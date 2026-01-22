package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/ejfkdev/go-volley"
)

var (
	timesMu        sync.Mutex
	completedTimes []time.Time
)

// ---------------- Server 端代码 ----------------

func startServer(port string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 当 Handler 被调用时，说明服务器终于收到了最后一个字节，解析出了完整的 HTTP 请求
		fmt.Printf("[Server] ✅ HTTP Request Processed | Src: %s | Time: %s\n",
			r.RemoteAddr, time.Now().Format("15:04:05.000000"))
		// 记录请求完成时间
		timesMu.Lock()
		completedTimes = append(completedTimes, time.Now())
		timesMu.Unlock()
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})

	server := &http.Server{
		Addr:    ":" + port,
		Handler: mux,
		// ConnState 用于监听底层的 TCP 连接状态
		ConnState: func(c net.Conn, state http.ConnState) {
			if state == http.StateNew {
				fmt.Printf("[Server] 🔌 TCP Connection New    | Src: %s | Time: %s\n",
					c.RemoteAddr().String(), time.Now().Format("15:04:05.000000"))
			}
		},
	}

	fmt.Printf("Server listening on port %s...\n", port)
	go func() {
		server.ListenAndServe()
	}()

	return server
}

// ---------------- Client 端代码 ----------------

func startClient(targetURL string) {
	// 1. 初始化我们的 Transport
	st := volley.NewTransport()
	client := &http.Client{
		Transport: st,
		Timeout:   60 * time.Second, // 设置超时防止死锁
	}

	var wg sync.WaitGroup
	requests := 10

	fmt.Println("\n[Client] 🚀 Starting Race Test...")

	// 2. 启动并发请求
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// 模拟发起请求
			// 这里的请求会发送 Headers + Body(N-1)，然后客户端在此处阻塞等待响应
			resp, err := client.Get(targetURL)
			if err != nil {
				fmt.Printf("[Client] #%d Error: %v\n", id, err)
				return
			}
			defer resp.Body.Close()
			io.ReadAll(resp.Body)
			// fmt.Printf("[Client] #%d Finished\n", id)
		}(i)

		// 关键点：我们在创建每个请求之间故意 sleep
		// 证明它们是在不同时间建立的连接，但会在同一时间被处理
		if i < requests-1 {
			fmt.Printf("[Client] Sleeping before next request...\n")
			time.Sleep(237 * time.Millisecond)
		}
	}

	// 3. 此时所有请求都已“预埋” (Header Straddling 状态)
	fmt.Println("[Client] ⏸️  All requests buffered. Waiting 1s before FIRE...")
	time.Sleep(1 * time.Second)

	// 4. 瞬时触发！
	fmt.Println("[Client] 🔥 FIRE! Releasing last bytes concurrently!", time.Now().Format("15:04:05.000000"))
	st.Fire()

	wg.Wait()
	fmt.Println("[Client] Test Done.")
}

func main() {
	// 为了演示方便，我们在同一个进程里跑 server 和 client
	// 实际使用中这通常是两个独立的程序
	port := "8765"

	server := startServer(port)

	// 给 server 一点启动时间
	time.Sleep(500 * time.Millisecond)

	target := "http://localhost:" + port + "/"
	startClient(target)

	// client 完成后，计算时间统计并优雅关闭 server
	timesMu.Lock()
	n := len(completedTimes)
	if n == 0 {
		fmt.Println("No requests were recorded by server.")
		timesMu.Unlock()
	} else {
		minT := completedTimes[0]
		maxT := completedTimes[0]
		for _, t := range completedTimes {
			if t.Before(minT) {
				minT = t
			}
			if t.After(maxT) {
				maxT = t
			}
		}
		diff := maxT.Sub(minT).Seconds()
		fmt.Printf("\n[Stats] Processed %d requests. Time span: %.6f seconds\n", n, diff)
		timesMu.Unlock()

		// 优雅关闭 server
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}
}
