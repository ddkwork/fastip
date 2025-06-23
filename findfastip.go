package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

func main() {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"),
	)

	ctx, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancel()

	ctx, cancel = chromedp.NewContext(ctx)
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var ips string
	err := chromedp.Run(ctx,
		chromedp.Navigate("https://www.itdog.cn/ping/github.com"),
		chromedp.Click(`//button[contains(text(),'单次测试')]`, chromedp.NodeVisible),
		chromedp.WaitVisible(`a.copy_ip`),
		chromedp.AttributeValue(`a.copy_ip`, "copy-text", &ips, nil),
	)

	if err != nil {
		log.Fatal(err)
	}

	// 提取IP并保存到host
	ipList := strings.Split(ips, "\n")
	var host []string

	fmt.Println("提取到的IP地址：")
	for i, ip := range ipList {
		ip = strings.TrimSpace(ip)
		if ip != "" {
			fmt.Printf("%2d: %s\n", i+1, ip)
			host = append(host, ip)
		}
	}

	fmt.Println("\n所有IP已保存到host变量")
	fmt.Printf("共提取到 %d 个有效IP\n", len(host))
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
