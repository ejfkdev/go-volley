# go-volley

go-volley — High-precision HTTP Race Condition Fuzzing Library for Go.

Golang 高精度并发同步库。

go-volley 是一个轻量级的 Go 语言网络库，用于在 HTTP/1.1 协议下实现微秒级的请求同步。它通过劫持底层的 TCP 连接，实现了 "Header Straddling"（或 TCP Last-Byte Sync）技术，允许你“预埋”数千个 HTTP 请求，并在同一时刻瞬间触发发送。它是 Turbo Intruder 核心技术的 Go 原生实现，且完全兼容 Go 标准库 `net/http` 及主流第三方库（如 `go-resty`）。 🚀

# 核心功能 (Features)

- 极高精度同步：基于 TCP/TLS 字节流控制，而非简单的协程等待，消除网络抖动（Jitter）影响。
- 完美兼容性：实现为标准的 `http.RoundTripper`，可直接插入 `http.Client` 或 Resty。
- 穿透力强：自动禁用 Keep-Alive，强制独立连接，绕过部分中间件的合并优化，直击后端逻辑。
- HTTPS 支持：在 TLS 握手后介入，精准控制解密后的 HTTP 报文最后一个字节。
- 零依赖：仅依赖 Go 标准库。

# 📦 安装 (Installation)

```bash
go get -u github.com/ejfkdev/go-volley
```

# ⚡️ 快速开始 (Quick Start)

```go
    vt := volley.NewTransport()
    client := &http.Client{Transport: vt}

    // 两个独立请求
    go client.Get("https://...")
    go client.Get("https://...")

    //
    vt.WaitHeldCount(context.Background(), 2)

    // 同时释放最后字节，发送完整请求
    vt.Fire()
```

## 1. 使用标准库 `net/http`

```go
package main

import (
    "fmt"
    "net/http"
    "sync"
    "time"

    "github.com/ejfkdev/go-volley"
)

func main() {
    // 1. 创建 Straddle Transport
    vt := volley.NewTransport()

    client := &http.Client{
        Transport: vt,
        Timeout:   10 * time.Second,
    }

    target := "http://127.0.0.1:8080/race-target"
    var wg sync.WaitGroup

    // 2. 预埋请求 (例如 20 个并发)
    for i := 0; i < 20; i++ {
        wg.Add(1)
        go func(id int) {
            defer wg.Done()
            resp, err := client.Get(target)
            if err != nil {
                fmt.Printf("[%d] Failed: %v\n", id, err)
                return
            }
            fmt.Printf("[%d] Status: %s\n", id, resp.Status)
            resp.Body.Close()
        }(i)
    }

    fmt.Println("Waiting for connections to be ready...")
    time.Sleep(2 * time.Second) // 等待所有连接建立完成

    // 等待20个请求预埋完毕
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := st.Wait(ctx, 20); err != nil {
		fmt.Printf("[Client] Wait error: %v\n", err)
	}

    // 4. 瞬时触发！
    fmt.Println("🔥 FIRE!")
    vt.Fire()

    wg.Wait()
}
```

## 2. 结合 `go-resty` 使用

```go
package main

import (
    "github.com/go-resty/resty/v2"
    "github.com/ejfkdev/go-volley"
)

func main() {
    vt := volley.NewTransport()

    client := resty.New()
    client.SetTransport(vt)

    // ... 发起并发请求，随后调用 st.Fire() ...
}
```

## 示例运行输出

<details>
<summary>go run examples/main.go</summary>

```
❯ go run examples/main.go
Server listening on port 8765...

[Client] 🚀 Starting Race Test...
[Client] Sleeping before next request...
[Server] 🔌 TCP Connection New    | Src: [::1]:65414 | Time: 17:46:25.448455
[Client] Sleeping before next request...
[Server] 🔌 TCP Connection New    | Src: [::1]:65415 | Time: 17:46:25.683751
[Client] Sleeping before next request...
[Server] 🔌 TCP Connection New    | Src: [::1]:65416 | Time: 17:46:25.921859
[Client] Sleeping before next request...
[Server] 🔌 TCP Connection New    | Src: [::1]:65417 | Time: 17:46:26.159538
[Client] Sleeping before next request...
[Server] 🔌 TCP Connection New    | Src: [::1]:65419 | Time: 17:46:26.397403
[Client] Sleeping before next request...
[Server] 🔌 TCP Connection New    | Src: [::1]:65420 | Time: 17:46:26.635324
[Client] Sleeping before next request...
[Server] 🔌 TCP Connection New    | Src: [::1]:65421 | Time: 17:46:26.872942
[Client] Sleeping before next request...
[Server] 🔌 TCP Connection New    | Src: [::1]:65422 | Time: 17:46:27.110572
[Client] Sleeping before next request...
[Server] 🔌 TCP Connection New    | Src: [::1]:65423 | Time: 17:46:27.348289
[Client] ⏸️  Waiting for all requests to be buffered...
[Server] 🔌 TCP Connection New    | Src: [::1]:65424 | Time: 17:46:27.586541
[Client] 🔥 FIRE! Releasing last bytes concurrently! 17:46:28.586176
[Server] ✅ HTTP Request Processed | Src: [::1]:65415 | Time: 17:46:28.586533
[Server] ✅ HTTP Request Processed | Src: [::1]:65419 | Time: 17:46:28.586550
[Server] ✅ HTTP Request Processed | Src: [::1]:65422 | Time: 17:46:28.586561
[Server] ✅ HTTP Request Processed | Src: [::1]:65424 | Time: 17:46:28.586578
[Server] ✅ HTTP Request Processed | Src: [::1]:65414 | Time: 17:46:28.586584
[Server] ✅ HTTP Request Processed | Src: [::1]:65420 | Time: 17:46:28.586562
[Server] ✅ HTTP Request Processed | Src: [::1]:65421 | Time: 17:46:28.586638
[Server] ✅ HTTP Request Processed | Src: [::1]:65416 | Time: 17:46:28.586797
[Server] ✅ HTTP Request Processed | Src: [::1]:65417 | Time: 17:46:28.586802
[Server] ✅ HTTP Request Processed | Src: [::1]:65423 | Time: 17:46:28.586846
[Client] Test Done.

[Stats] Processed 10 requests. Time span: 0.000335 seconds

```

</details>

# 🧠 技术原理 (How it Works)

传统的并发测试（如使用 Goroutines）受限于 Go 调度器（Scheduler）和系统网络栈的微小延迟，请求到达服务器的时间往往分散在几毫秒甚至几十毫秒内。这对于极短时间窗口的竞争条件（Race Condition）探测是不够的。

go-volley 的工作流程：

1. 建立连接：针对每个请求建立独立的 TCP/TLS 连接。
2. 预埋 (Straddling)：将 HTTP 请求的前 $N-1$ 个字节发送给服务器。
3. 扣留 (Holding)：在内存中拦截并扣留最后一个字节（通常是 Body 的最后一位或 Header 的换行符）。此时服务器已收到大部分数据，线程处于 Read() 阻塞等待状态。
4. 同步 (Gate)：所有 Goroutine 进入等待状态。
5. 触发 (Fire)：调用 `Fire()`，库会在同一微秒内并发写入所有连接的最后 1 个字节。结果：服务器瞬间收到所有请求的完整数据，并在极短的时间窗内并发处理业务逻辑。

兼容性

- 与 Go 标准库 `net/http`、`http.Client` 及常见第三方库（例如 `go-resty`）兼容。

贡献

- 欢迎提交 PR、Issue 和改进建议。

License

MIT
