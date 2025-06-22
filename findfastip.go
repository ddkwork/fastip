package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

const (
	dohServer   = "https://cloudflare-dns.com/dns-query" // DoH服务器
	testFileURL = "https://github.com/favicon.ico"       // 测试文件
)

type RegionInfo struct {
	Region     string `json:"region"`
	Country    string `json:"country"`
	City       string `json:"city,omitempty"`
	GitHubEdge string `json:"github_edge,omitempty"`
	ResolverIP string `json:"resolver_ip,omitempty"`
}

func main() {
	log.Println("Starting Smart GitHub CDN Optimizer")

	// 第一步：确定Action所在区域
	regionInfo, err := detectActionRegion()
	if err != nil {
		log.Fatalf("区域检测失败: %v", err)
	}

	// 第二步：获取该区域的GitHub CDN节点
	nodes, err := getGitHubCDNNodes(regionInfo)
	if err != nil {
		log.Fatalf("获取CDN节点失败: %v", err)
	}

	// 第三步：测试并选择最优节点
	bestNode := findBestCDNNode(nodes)

	// 第四步：验证并保存结果
	if validateCDNNode(bestNode) {
		saveResults(regionInfo, bestNode)
		log.Printf("找到最优CDN节点: %s (延迟: %.2fms, 速度: %.2fKB/s)",
			bestNode.IP, bestNode.Latency, bestNode.Speed)
	} else {
		log.Println("没有找到有效CDN节点，使用默认配置")
		saveFallbackResult()
	}
}

// 检测Action运行区域
func detectActionRegion() (RegionInfo, error) {
	info := RegionInfo{}

	// 从GitHub API获取区域信息
	resp, err := http.Get("https://api.github.com/meta")
	if err == nil {
		defer resp.Body.Close()
		var meta struct {
			Runners map[string][]string `json:"actions"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&meta); err == nil {
			// 从实例元数据中获取区域信息
			ec2Resp, err := http.Get("http://169.254.169.254/latest/meta-data/placement/availability-zone")
			if err == nil {
				defer ec2Resp.Body.Close()
				if az, err := io.ReadAll(ec2Resp.Body); err == nil {
					azStr := string(az)
					info.Region = azStr[:len(azStr)-1] // 移除可用区后缀
				}
			}
		}
	}

	// 使用IP定位服务
	if info.Region == "" {
		ipInfoResp, err := http.Get("https://ipinfo.io/json")
		if err == nil {
			defer ipInfoResp.Body.Close()
			var ipInfo struct {
				Country string `json:"country"`
				Region  string `json:"region"`
				City    string `json:"city"`
				Org     string `json:"org"`
			}
			if err := json.NewDecoder(ipInfoResp.Body).Decode(&ipInfo); err == nil {
				info.Country = ipInfo.Country
				info.Region = ipInfo.Region
				info.City = ipInfo.City

				// 检测是否在GitHub自有基础设施上运行
				if strings.Contains(ipInfo.Org, "GitHub") {
					info.GitHubEdge = "self-hosted"
				}
			}
		}
	}

	// 获取本地DNS解析器
	if resolvConf, err := os.Open("/etc/resolv.conf"); err == nil {
		defer resolvConf.Close()
		if content, err := io.ReadAll(resolvConf); err == nil {
			for _, line := range strings.Split(string(content), "\n") {
				if strings.HasPrefix(line, "nameserver") {
					parts := strings.Fields(line)
					if len(parts) > 1 {
						info.ResolverIP = parts[1]
						break
					}
				}
			}
		}
	}

	return info, nil
}

// 使用DNS-over-HTTPS获取GitHub CDN节点
func getGitHubCDNNodes(region RegionInfo) ([]CDNNode, error) {
	var nodes []CDNNode

	// 使用DNS-over-HTTPS查询
	client := new(dns.Client)
	msg := new(dns.Msg)
	msg.SetQuestion("github.com.", dns.TypeA)
	msg.SetEdns0(4096, true) // 启用EDNS0

	// 如果知道区域信息，添加ECS扩展
	if region.ResolverIP != "" {
		ecs := new(dns.EDNS0_SUBNET)
		ecs.Code = dns.EDNS0SUBNET
		ecs.Family = 1 // IPv4
		ecs.SourceNetmask = 24
		if ip := net.ParseIP(region.ResolverIP); ip != nil {
			ecs.Address = ip
			opt := new(dns.OPT)
			opt.Hdr.Name = "."
			opt.Hdr.Rrtype = dns.TypeOPT
			opt.Option = append(opt.Option, ecs)
			msg.Extra = append(msg.Extra, opt)
		}
	}

	// 执行DNS查询
	resp, _, err := client.Exchange(msg, dohServer)
	if err != nil {
		return nil, err
	}

	// 解析结果
	for _, ans := range resp.Answer {
		if a, ok := ans.(*dns.A); ok {
			nodes = append(nodes, CDNNode{
				IP:     a.A.String(),
				Region: region.Region,
			})
		}
	}

	// 特殊处理已知区域的CDN边缘
	switch region.Region {
	case "ap-southeast-1": // 新加坡
		nodes = append(nodes, CDNNode{IP: "13.229.188.59"}) // GitHub SG CDN
	case "ap-northeast-1": // 东京
		nodes = append(nodes, CDNNode{IP: "13.114.40.48"}) // GitHub JP CDN
	case "eu-central-1": // 法兰克福
		nodes = append(nodes, CDNNode{IP: "18.184.176.26"}) // GitHub DE CDN
	}

	return nodes, nil
}

type CDNNode struct {
	IP      string
	Region  string
	Latency float64
	Speed   float64
}

// 测试并选择最优CDN节点
func findBestCDNNode(nodes []CDNNode) CDNNode {
	if len(nodes) == 0 {
		return CDNNode{IP: "140.82.113.3"} // 默认回退
	}

	// 并发测试所有节点
	var wg sync.WaitGroup
	results := make(chan CDNNode, len(nodes))

	for _, node := range nodes {
		wg.Add(1)
		go func(n CDNNode) {
			defer wg.Done()
			n.Latency = testLatency(n.IP)
			if n.Latency > 0 {
				n.Speed = testDownloadSpeed(n.IP)
			}
			results <- n
		}(node)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	bestNode := CDNNode{Latency: 1000}
	for node := range results {
		if node.Latency > 0 && node.Latency < bestNode.Latency {
			bestNode = node
		}
	}

	return bestNode
}

func testLatency(ip string) float64 {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", ip+":443", 2*time.Second)
	if err != nil {
		return -1
	}
	defer conn.Close()
	return time.Since(start).Seconds() * 1000 // ms
}

func testDownloadSpeed(ip string) float64 {
	client := http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.Dial("tcp", ip+":443")
			},
		},
		Timeout: 5 * time.Second,
	}

	start := time.Now()
	resp, err := client.Get(testFileURL)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	_, err = io.Copy(io.Discard, resp.Body)
	if err != nil {
		return 0
	}

	duration := time.Since(start).Seconds()
	sizeKB := float64(4500) / 1024
	return sizeKB / duration
}

func validateCDNNode(node CDNNode) bool {
	return node.Latency > 0 && node.Latency < 500 && node.Speed > 100
}

func saveResults(region RegionInfo, node CDNNode) {
	// 创建结果目录
	os.Mkdir("results", 0755)

	// 保存最优节点
	os.WriteFile("results/best_cdn.txt", []byte(node.IP), 0644)

	// 保存完整报告
	report := struct {
		Timestamp string     `json:"timestamp"`
		Region    RegionInfo `json:"region"`
		BestCDN   CDNNode    `json:"best_cdn"`
	}{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Region:    region,
		BestCDN:   node,
	}

	if data, err := json.MarshalIndent(report, "", "  "); err == nil {
		os.WriteFile("results/cdn_report.json", data, 0644)
	}
}

func saveFallbackResult() {
	os.WriteFile("results/best_cdn.txt", []byte("geo-optimized"), 0644)
}
