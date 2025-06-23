package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	itdogURL = "https://www.itdog.cn/tc/ping/"
	timeout  = 20 * time.Second
)

var domains = []string{
	"github.com",
	"raw.githubusercontent.com",
	"github.global.ssl.fastly.net",
	"assets-cdn.github.com",
}

type PingResult struct {
	Data struct {
		NodeList []struct {
			NodeName string  `json:"node_name"`
			IP       string  `json:"ip"`
			Timeout  int     `json:"timeout"`
			Time     []int   `json:"time"`
			AvgTime  float64 `json:"avg_time"`
		} `json:"node_list"`
	} `json:"data"`
}

func main() {
	// 获取最优IP映射
	ipMap := make(map[string]string)
	for _, domain := range domains {
		if ip, err := getBestIP(domain); err == nil {
			fmt.Printf("✅ 域名: %-30s 最优IP: %s\n", domain, ip)
			ipMap[domain] = ip
		} else {
			fmt.Printf("❌ 域名: %s 错误: %v\n", domain, err)
		}
	}

	// 更新hosts文件
	if len(ipMap) > 0 {
		if err := updateHosts(ipMap); err != nil {
			fmt.Println("❌ 更新hosts文件失败:", err)
		}
	}

	// 刷新DNS缓存
	flushDNS()
	fmt.Println("\n操作完成，GitHub访问已加速！🚀")
}

// 获取最优IP
func getBestIP(domain string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 构建请求
	payload := fmt.Sprintf("host=%s&number=2", domain)
	req, err := http.NewRequestWithContext(ctx, "POST", itdogURL, bytes.NewBufferString(payload))
	if err != nil {
		return "", err
	}

	// 模拟浏览器请求
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", "https://www.itdog.cn/tc/ping/")

	// 发送请求
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// 读取响应
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// 解析JSON
	var result PingResult
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("JSON解析错误: %v", err)
	}

	// 分析测试结果
	return findFastestIP(result, domain)
}

// 查找最快IP
func findFastestIP(result PingResult, domain string) (string, error) {
	var bestIP string
	minAvg := 1000.0 // 设置较大的初始值

	ips := make(map[string][]float64) // IP到延迟列表的映射

	// 收集所有IP的延迟数据
	for _, node := range result.Data.NodeList {
		// 过滤超时结果
		if node.Timeout > 0 {
			continue
		}

		// 仅处理包含中文城市名称的节点（国内节点）
		if strings.ContainsAny(node.NodeName, "北京上海广州深圳成都") {
			ips[node.IP] = append(ips[node.IP], node.AvgTime)
		}
	}

	// 计算平均延迟并找出最优IP
	for ip, delays := range ips {
		var sum float64
		for _, d := range delays {
			sum += d
		}
		avg := sum / float64(len(delays))

		if avg < minAvg {
			minAvg = avg
			bestIP = ip
		}
	}

	if bestIP == "" {
		return "", fmt.Errorf("未找到低延迟的国内IP")
	}

	// 验证IP是否有效
	if parsedIP := net.ParseIP(bestIP); parsedIP == nil {
		return "", fmt.Errorf("无效IP地址: %s", bestIP)
	}

	return bestIP, nil
}

// 更新hosts文件
func updateHosts(ipMap map[string]string) error {
	// 根据操作系统确定hosts文件路径
	var hostsPath string
	switch runtime.GOOS {
	case "windows":
		hostsPath = `C:\Windows\System32\drivers\etc\hosts`
	case "linux", "darwin": // darwin是macOS
		hostsPath = "/etc/hosts"
	default:
		return fmt.Errorf("不支持的操作系统: %s", runtime.GOOS)
	}

	// 读取现有hosts文件
	file, err := os.Open(hostsPath)
	if err != nil {
		return err
	}
	defer file.Close()

	var newLines []string
	scanner := bufio.NewScanner(file)
	existingDomains := make(map[string]bool)

	// 处理每一行
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// 保留注释行
		if strings.HasPrefix(line, "#") {
			newLines = append(newLines, line)
			continue
		}

		// 解析主机行
		fields := strings.Fields(line)
		if len(fields) < 2 {
			newLines = append(newLines, line)
			continue
		}

		// 检查是否是需要更新的域名
		updated := false
		for i := 1; i < len(fields); i++ {
			domain := fields[i]
			if newIP, exists := ipMap[domain]; exists {
				if fields[0] != newIP {
					// 构建更新行
					newLine := newIP + " " + strings.Join(fields[1:], " ")
					newLines = append(newLines, newLine)
					fmt.Printf("🔄 更新: %s -> %s\n", domain, newIP)
				} else {
					fmt.Printf("✅ 无需更新: %s 已是最新\n", domain)
					newLines = append(newLines, line)
				}
				updated = true
				existingDomains[domain] = true
				break
			}
		}

		if !updated {
			newLines = append(newLines, line)
		}
	}

	// 添加缺失的域名条目
	for domain, ip := range ipMap {
		if !existingDomains[domain] {
			newLine := fmt.Sprintf("%s %s", ip, domain)
			newLines = append(newLines, newLine)
			fmt.Printf("➕ 新增: %s -> %s\n", domain, ip)
		}
	}

	// 写入更新后的hosts文件
	output, err := os.Create(hostsPath)
	if err != nil {
		return err
	}
	defer output.Close()

	writer := bufio.NewWriter(output)
	for _, line := range newLines {
		fmt.Fprintln(writer, line)
	}
	writer.Flush()

	return nil
}

// 刷新DNS缓存
func flushDNS() {
	fmt.Println("\n刷新DNS缓存...")
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("ipconfig", "/flushdns")
	case "darwin": // macOS
		cmd = exec.Command("sudo", "killall", "-HUP", "mDNSResponder")
	case "linux":
		// 尝试不同的Linux刷新方法
		if _, err := exec.LookPath("resolvectl"); err == nil {
			cmd = exec.Command("sudo", "resolvectl", "flush-caches")
		} else {
			cmd = exec.Command("sudo", "systemd-resolve", "--flush-caches")
		}
	default:
		fmt.Println("⚠️ 不支持的操作系统，请手动刷新DNS")
		return
	}

	if err := cmd.Run(); err != nil {
		fmt.Printf("⚠️ 刷新DNS失败: %v (可能需要sudo权限)\n", err)
	} else {
		fmt.Println("✅ DNS缓存刷新完成")
	}
}
