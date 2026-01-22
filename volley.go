package volley

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// --- Straddle Transport (核心控制器) ---

// Transport 是用于控制“字节扣留（straddle）”行为的自定义 http.Transport 封装。
//
// 它包装了一个 `http.Transport`，在底层连接上使用 `StraddleConn` 以在写入时
// 保留最后一个字节，直到显式调用 `Fire()` 或连接关闭时释放该字节。
//
// 注意: 默认 TLS 配置在示例中启用了 `InsecureSkipVerify: true` —— 请在生产中不要这样做。
type Transport struct {
	*http.Transport
	conns []*StraddleConn
	mu    sync.Mutex
}

// NewTransport 创建一个用于并发控制的 Transport
func NewTransport() *Transport {
	t := &Transport{
		conns: make([]*StraddleConn, 0),
	}

	// 初始化底层的 http.Transport
	t.Transport = &http.Transport{
		ForceAttemptHTTP2: false, // 必须禁用 HTTP/2
		// 强制禁用 KeepAlive，确保每个请求使用独立的 TCP 连接（源端口不同）
		DisableKeepAlives:   true,
		MaxIdleConnsPerHost: -1,

		TLSClientConfig: &tls.Config{
			NextProtos:         []string{"http/1.1"},
			InsecureSkipVerify: true,
		},

		// 劫持 HTTPS (TLS) 连接
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			rawConn, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}

			// 设置 TLS 配置并确保 handshake 可被 ctx 取消
			tlsConfig := t.Transport.TLSClientConfig.Clone()
			if tlsConfig.ServerName == "" {
				tlsConfig.ServerName = strings.Split(addr, ":")[0]
			}

			if dl, ok := ctx.Deadline(); ok {
				_ = rawConn.SetDeadline(dl)
			} else {
				_ = rawConn.SetDeadline(time.Now().Add(15 * time.Second))
			}

			tlsConn := tls.Client(rawConn, tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				rawConn.Close()
				return nil, err
			}

			_ = rawConn.SetDeadline(time.Time{})

			return registerStraddleConn(newStraddleConn(tlsConn), t), nil
		},

		// 劫持 HTTP (纯 TCP) 连接
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			rawConn, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return registerStraddleConn(newStraddleConn(rawConn), t), nil
		},
	}
	return t
}

func (t *Transport) register(c *StraddleConn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c.owner = t
	t.conns = append(t.conns, c)
}

func (t *Transport) unregister(c *StraddleConn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, cc := range t.conns {
		if cc == c {
			// remove without preserving order
			t.conns = append(t.conns[:i], t.conns[i+1:]...)
			return
		}
	}
}

// Fire 释放所有连接中被扣留的字节
func (t *Transport) Fire() {
	t.mu.Lock()
	currentConns := t.conns
	t.conns = make([]*StraddleConn, 0) // 重置队列
	t.mu.Unlock()

	// 并发释放以保证瞬时性
	for _, conn := range currentConns {
		go conn.release()
	}
}

// --- Straddle Conn (连接包装器) ---

type StraddleConn struct {
	net.Conn
	held    byte // 暂存的最后一个字节
	hasHeld bool
	owner   *Transport
	mu      sync.Mutex
}

func newStraddleConn(c net.Conn) *StraddleConn {
	return &StraddleConn{Conn: c}
}

// registerStraddleConn 将 StraddleConn 注册到 Transport 并返回该连接。
// 提取为函数以减少重复注册代码。
func registerStraddleConn(sc *StraddleConn, t *Transport) *StraddleConn {
	t.register(sc)
	return sc
}

// Write 拦截写入操作，实现“滚动缓冲”策略
func (sc *StraddleConn) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()

	// 1. 构造完整 payload：上次扣留的 + 这次写入的
	var payload []byte
	if sc.hasHeld {
		payload = append(payload, sc.held)
		sc.hasHeld = false
	}
	payload = append(payload, b...)

	if len(payload) == 0 {
		return len(b), nil
	}

	// 2. 扣留最后一个字节
	toHold := payload[len(payload)-1]
	sc.held = toHold
	sc.hasHeld = true

	// 3. 发送前面的 N-1 个字节
	toSend := payload[:len(payload)-1]
	if len(toSend) > 0 {
		_, err := sc.Conn.Write(toSend)
		if err != nil {
			return 0, err
		}
	}

	// 4. 欺骗上层：即使最后一个字节没发，也返回全部写入成功
	return len(b), nil
}

// release 手动释放最后扣留的字节
func (sc *StraddleConn) release() {
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if sc.hasHeld {
		_, err := sc.Conn.Write([]byte{sc.held})
		if err != nil {
			fmt.Printf("Release error: %v\n", err)
		}
		sc.hasHeld = false
	}
}

// Close releases any held byte, closes the underlying connection and
// notifies the owning Transport to unregister this connection.
func (sc *StraddleConn) Close() error {
	sc.mu.Lock()
	// release any held byte while holding the lock
	if sc.hasHeld {
		_, _ = sc.Conn.Write([]byte{sc.held})
		sc.hasHeld = false
	}
	sc.mu.Unlock()

	// close underlying connection
	err := sc.Conn.Close()

	// notify owner to remove reference
	if sc.owner != nil {
		sc.owner.unregister(sc)
	}
	return err
}
