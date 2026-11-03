// tlsfingerprint.go 让出站 TLS 的 ClientHello 与官方桌面客户端一致（JA3/JA4）。
//
// 为什么只在这一层较真：头顺序、body 键序在服务端反序列化之后都不存在，
// 中间设备不解密时也只能看包长；**TLS ClientHello 是唯一「握手前明文、上游必然
// 可见」的指纹层**。Go 标准库默认形态是 t13d131100_f57a46bbacb6_f50d94e863eb
// （13 cipher / 11 扩展），与官方完全不是一族——一次 JA4 就能筛出来。
//
// 官方形态（三份独立实现的抓包结论一致，2026-11-01 抓包，声明 420 次握手逐字节
// 一致：workbuddy2api-android 954455b8 / ae12f35c、workbuddy2api-panel 5402cc94、
// workbuddy2api-imgen-doubao 15ac5c05）：
//
//	JA3  771,4865-4866-4867-49199-49195-49200-49196-49191-52393-52392-49161-49171-49162-49172-156-157-47-53,0-23-65281-10-11-35-13-51-45-43,29-23-24,0
//	JA4  t13d0a1200_5d04281c6031_b76c5ee2507d
//
// 关键特征是**没有 ALPN、没有 padding**、只有 10 个扩展（Node 主进程形态）。
// 这与本项目「禁 h2」的既有策略天然同向：既然不协商 h2，就不该在 ClientHello
// 里声明 ALPN。
package upstream

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"

	utls "github.com/refraction-networking/utls"
)

// officialCipherSuites 官方 ClientHello 的 cipher 列表（顺序即字节序，不能排序）。
//
// 十进制对照（JA3 用十进制书写，这里用十六进制定义）：
//
//	4865=0x1301 4866=0x1302 4867=0x1303 49199=0xc02f 49195=0xc02b
//	49200=0xc030 49196=0xc02c 49191=0xc027 52393=0xcca9 52392=0xcca8
//	49161=0xc009 49171=0xc013 49162=0xc00a 49172=0xc014
//	156=0x009c 157=0x009d 47=0x002f 53=0x0035
var officialCipherSuites = []uint16{
	0x1301, 0x1302, 0x1303, // TLS 1.3
	0xc02f, 0xc02b, 0xc030, 0xc02c, 0xc027, // ECDHE-RSA/ECDSA AES-GCM / AES-CBC-SHA256
	0xcca9, 0xcca8, // ChaCha20-Poly1305
	0xc009, 0xc013, 0xc00a, 0xc014, // ECDHE CBC-SHA
	0x009c, 0x009d, // RSA AES-GCM
	0x002f, 0x0035, // RSA CBC-SHA
}

// officialJA3 官方 JA3 原文，仅用于测试回归断言（逐字节钉住 ClientHello 的
// cipher 列表、扩展出现顺序、曲线顺序与点格式）。
const officialJA3 = "771,4865-4866-4867-49199-49195-49200-49196-49191-52393-52392-49161-49171-49162-49172-156-157-47-53,0-23-65281-10-11-35-13-51-45-43,29-23-24,0"

// officialJA3MD5 官方 JA3 的 md5（社区与多数风控面板用的就是这个值）：
// 71dc8c533dd919ae9f4963224a4ba8fd。CN 与 global 同一套。
// OfficialJA3MD5 是 officialJA3MD5 的导出形式（启动日志用）。
const OfficialJA3MD5 = officialJA3MD5

const officialJA3MD5 = "71dc8c533dd919ae9f4963224a4ba8fd"

// officialSignatureAlgorithms 官方 signature_algorithms 列表。
// 它不影响 JA3（JA3 只记扩展 ID 13），但决定服务器选哪张证书/哪种签名——
// 留空会让服务端按自身默认挑，可能与官方路径不一致（例如给 EC 证书而我们
// 声明的偏好完全不同）。按抓包顺序照抄。
var officialSignatureAlgorithms = []utls.SignatureScheme{
	0x0403, // ECDSA P-256 + SHA256
	0x0804, // RSA-PSS + SHA256
	0x0401, // RSA PKCS#1 + SHA256
	0x0503, // ECDSA P-384 + SHA384
	0x0805, // RSA-PSS + SHA384
	0x0501, // RSA PKCS#1 + SHA384
	0x0806, // RSA-PSS + SHA512
	0x0601, // RSA PKCS#1 + SHA512
	0x0201, // RSA PKCS#1 + SHA1
}

// officialClientHelloSpec 构造官方的 ClientHello：
// 10 个扩展、顺序固定、**无 ALPN、无 padding**、key_share 只带 x25519。
//
// 扩展顺序错 JA3 的扩展段就对不上（JA3 用扩展出现序），所以这里逐项标注 ID。
func officialClientHelloSpec() *utls.ClientHelloSpec {
	return &utls.ClientHelloSpec{
		TLSVersMin:         tls.VersionTLS12,
		TLSVersMax:         tls.VersionTLS13,
		CipherSuites:       officialCipherSuites,
		CompressionMethods: []byte{0},
		Extensions: []utls.TLSExtension{
			&utls.SNIExtension{},                      // 0
			&utls.UtlsExtendedMasterSecretExtension{}, // 23
			&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient},                       // 65281
			&utls.SupportedCurvesExtension{Curves: []utls.CurveID{utls.X25519, utls.CurveP256, utls.CurveP384}}, // 10
			&utls.SupportedPointsExtension{SupportedPoints: []byte{0}},                                          // 11
			&utls.SessionTicketExtension{}, // 35
			&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: officialSignatureAlgorithms}, // 13
			&utls.KeyShareExtension{KeyShares: []utls.KeyShare{{Group: utls.X25519}}},                     // 51
			&utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}},                           // 45
			&utls.SupportedVersionsExtension{Versions: []uint16{tls.VersionTLS13, tls.VersionTLS12}},      // 43
		},
	}
}

// proxyConfigured 报告环境里是否配了出站代理（含 NO_PROXY 语义，与实际拨号口径一致）。
//
// 为什么必须知道：一旦设了 DialTLSContext，net/http 会**跳过代理**直接连目标
// （Go 的 dialConn 先看 hasCustomTLSDialer 再处理 proxy）。不做这个判断就会
// 静默绕过代理——既破坏用户配置的出口，也让「一条连接一个身份」的前提失效。
// 有代理时不装自定义拨号，指纹退回 Go 默认形态（代价明确，但代理仍然生效）。
func proxyConfigured() bool {
	req, err := http.NewRequest(http.MethodGet, "https://upstream-probe.invalid/", nil)
	if err != nil {
		return false
	}
	u, err := http.ProxyFromEnvironment(req)
	return err == nil && u != nil
}

// newTLSDialer 返回自定义 TLS 拨号函数：TCP 用给定 dialer，TLS 用官方 ClientHello。
// 返回的连接已握手完成，可直接交给 http.Transport 的 DialTLSContext。
func newTLSDialer(d *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if d == nil {
		d = newDialer()
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialTLSNode(ctx, d, network, addr)
	}
}

// dialTLSNode 建 TCP → 用官方 ClientHello 握手 → 返回已完成的连接。
//
// 证书校验沿用标准库语义（InsecureSkipVerify=false + ServerName），uTLS 的握手
// 状态机派生自 crypto/tls，验证路径一致。
//
// 握手超时必须自己做：Transport.TLSHandshakeTimeout 只作用于它自己发起的握手，
// 自定义 DialTLSContext 不在其管辖范围（不设就等于没有上限）。
func dialTLSNode(ctx context.Context, d *net.Dialer, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	raw, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return handshakeOfficialHello(ctx, raw, host)
}

// handshakeOfficialHello 在已建立的 raw 连接上用官方 ClientHello 完成握手。
// 单独拆出来是为了让测试能用 net.Pipe 直接观察发出去的字节（不需要真实端口）。
func handshakeOfficialHello(ctx context.Context, raw net.Conn, host string) (net.Conn, error) {
	hctx, cancel := context.WithTimeout(ctx, tlsHandshakeTimeout)
	defer cancel()

	cfg := &utls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if testTLSConfigHook != nil {
		// 测试注入根证书用（httptest 自签），生产恒为 nil。
		cfg = testTLSConfigHook(host)
	}
	uconn := utls.UClient(raw, cfg, utls.HelloCustom)
	if err := uconn.ApplyPreset(officialClientHelloSpec()); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("upstream tls: apply preset: %w", err)
	}
	if err := uconn.HandshakeContext(hctx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("upstream tls: handshake: %w", err)
	}
	return uconn, nil
}

// testTLSConfigHook 仅供测试注入根证书；生产恒为 nil。
var testTLSConfigHook func(host string) *utls.Config

// TLSFingerprintActive 报告出站是否真的在用官方 ClientHello（有代理时不用）。
// 供启动日志用：出问题时第一眼看的就是这一行。
func TLSFingerprintActive() bool { return !proxyIsConfigured() }

// proxyIsConfigured 是 proxyConfigured 的可替换引用：测试需要在同一台机器上
// 同时覆盖「有代理 / 无代理」两条分支，而系统级代理（Windows 注册表）无法用
// 环境变量撤销。生产恒等于 proxyConfigured。
var proxyIsConfigured = proxyConfigured

// attachTLSDialer 给 Transport 装上自定义 TLS 拨号（有代理时不装，见 proxyConfigured），
// 并保证切到有代理时把已装的那份清掉。
func attachTLSDialer(tr *http.Transport, d *net.Dialer) {
	if tr == nil {
		return
	}
	if proxyIsConfigured() {
		// 有代理：交给 net/http 自己 CONNECT + 握手（指纹退回 Go 默认形态，
		// 但代理仍然生效——两害相权，绕过代理更糟）。
		tr.DialTLSContext = nil
		return
	}
	tr.DialTLSContext = newTLSDialer(d)
}
