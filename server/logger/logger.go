package logger

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type logger struct {
	mu        sync.Mutex
	logDir    string
	files     map[string]*os.File // "info", "error", "audit"
	done      chan struct{}
	closeOnce sync.Once
}

// 全局 logger 指针。
//
// v21.202（CI run37958794289 backend job `go test -race` 抓到的真实 data race）：
// 此前是**包级裸指针** `var l *logger`，而 `Init` 的24h 定时清理 goroutine 在
// select 里裸读 `l.done`，测试串行调用第二次 `Init` 又裸写 `l` ⇒
// 写（logger.go:28）与读（logger.go:45）并发，构成 data race。
//
// 产品侧同样受影响：`Init` 可在 `Close` 之后被再次调用（热重载/测试/未来多实例），
// 裸指针没有任何同步保证。
//
// 修法：`atomic.Pointer` 承载 —— 读侧（writeRaw/Close）一次 Load 即得一致快照，
// 写侧（Init）先关停旧实例再 Store。ticker goroutine 改为**闭包捕获自己的 done 通道**，
// 不再读全局，从根上消除该读点。
var l atomic.Pointer[logger]

// cur 返回当前 logger 实例（可能为 nil）。所有读点统一走这里。
func cur() *logger { return l.Load() }

func Init(logDir string) error {
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	// 重复 Init 时先关停上一实例：关停后再替换指针，
	// 避免旧 ticker goroutine 与新实例并存（旧 done 通道必须先close）。
	if old := l.Load(); old != nil {
		old.closeOnce.Do(func() {
			close(old.done)
			old.mu.Lock()
			defer old.mu.Unlock()
			for _, f := range old.files {
				f.Close()
			}
		})
	}

	inst := &logger{
		logDir: logDir,
		files:  make(map[string]*os.File),
		done:   make(chan struct{}),
	}
	l.Store(inst)

	// 立即清理一次
	cleanOldLogs(logDir, 30)

	// 启动定时清理（每24小时）。
	// ⚠ 闭包只捕获 inst.done（不再读全局 l）——这是本次 race 的读点根因。
	// inst 只在 Init 内被创建一次且不再改写指针字段，捕获它是安全的。
	go func(inst *logger) {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cleanOldLogs(logDir, 30)
			case <-inst.done:
				return
			}
		}
	}(inst)

	return nil
}

func Close() {
	inst := cur()
	if inst == nil {
		return
	}
	inst.closeOnce.Do(func() {
		close(inst.done)
		inst.mu.Lock()
		defer inst.mu.Unlock()
		for _, f := range inst.files {
			f.Close()
		}
	})
}

func Info(format string, args ...interface{}) {
	write("INFO", format, args...)
}

func Error(format string, args ...interface{}) {
	write("ERROR", format, args...)
}

// InfoS 结构化 Info 日志（v19.3 优-9）：key=value 文本格式，便于 grep。
// 旧 Info/Error 保持 printf 格式不变（用户确认不改调用方），
// 新代码（请求日志中间件等）使用 InfoS/ErrorS 获得结构化输出。
func InfoS(msg string, keysAndValues ...interface{}) {
	writeRaw("INFO", formatSlog(msg, keysAndValues...))
}

func ErrorS(msg string, keysAndValues ...interface{}) {
	writeRaw("ERROR", formatSlog(msg, keysAndValues...))
}

// formatSlog 构造 key=value 文本格式的结构化日志行。
// 示例: msg="request completed" method=GET path=/notes status=200 duration=5ms
func formatSlog(msg string, keysAndValues ...interface{}) string {
	var b strings.Builder
	b.WriteString(`msg="` + msg + `"`)
	for i := 0; i+1 < len(keysAndValues); i += 2 {
		k := fmt.Sprint(keysAndValues[i])
		v := fmt.Sprint(keysAndValues[i+1])
		b.WriteString(" " + k + "=" + v)
	}
	return b.String()
}

// AuditEntry 结构化审计日志条目（v18.9，优-6）：audit-*.log 每行一条 JSON（JSON Lines），
// 固定字段便于 grep/jq 与后续接入日志管道。action 用蛇形动词（login/register/...），
// result 仅取 success/failed/blocked 三值，detail 放自由文本补充。
type AuditEntry struct {
	Time   string `json:"time"`
	Action string `json:"action"`
	Actor  string `json:"actor"`
	IP     string `json:"ip"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
}

// AuditEvent 写一条结构化审计日志。旧 Audit(format, ...) 已移除——
// 全部调用点强制走结构化字段，避免同一文件混入两种格式。
func AuditEvent(action, actor, ip, result, detail string) {
	e := AuditEntry{
		Time:   time.Now().Format("2006-01-02 15:04:05"),
		Action: action,
		Actor:  actor,
		IP:     ip,
		Result: result,
		Detail: detail,
	}
	b, err := json.Marshal(e)
	if err != nil {
		write("ERROR", "audit marshal failed: %v", err)
		return
	}
	writeRaw("AUDIT", string(b))
}

func write(level, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	writeRaw(level, msg)
}

func writeRaw(level, msg string) {
	// v21.202：一次 Load 取一致快照（原先裸读全局指针，与 Init 的写构成 race）。
	inst := cur()
	if inst == nil {
		// logger 未初始化，降级到 stdout
		ts := time.Now().Format("2006-01-02 15:04:05")
		fmt.Printf("%s [%s] %s\n", ts, level, msg)
		return
	}

	ts := time.Now().Format("2006-01-02 15:04:05")
	line := fmt.Sprintf("%s [%s] %s\n", ts, level, msg)

	// 输出到 stdout
	fmt.Print(line)

	// 输出到文件
	inst.mu.Lock()
	defer inst.mu.Unlock()

	f := inst.getFile(level)
	if f != nil {
		f.WriteString(line)
	}
}

func (l *logger) getFile(level string) *os.File {
	key := strings.ToLower(level)
	today := time.Now().Format("2006-01-02")

	// 检查当前文件是否是今天的
	if f, ok := l.files[key]; ok {
		if f.Name() == filepath.Join(l.logDir, fmt.Sprintf("%s-%s.log", key, today)) {
			return f
		}
		// 日期变了，关闭旧文件
		f.Close()
	}

	// 打开今天的文件
	// v21.90 P2（审计 L-03）：0644 → 0600——日志含审计事件（用户名/IP），
	// 父目录 0700 已隔离同机其他用户，此处收敛文件权限为仅属主可读写。
	filename := filepath.Join(l.logDir, fmt.Sprintf("%s-%s.log", key, today))
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open log file %s: %v\n", filename, err)
		return nil
	}

	l.files[key] = f
	return f
}

func cleanOldLogs(logDir string, days int) {
	cutoff := time.Now().AddDate(0, 0, -days)
	patterns := []string{"info-*.log", "error-*.log", "audit-*.log"}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(logDir, pattern))
		if err != nil {
			continue
		}
		for _, path := range matches {
			base := filepath.Base(path)
			// 从文件名提取日期，格式: level-2006-01-02.log
			parts := strings.SplitN(base, "-", 2)
			if len(parts) < 2 {
				continue
			}
			dateStr := strings.TrimSuffix(parts[1], ".log")
			t, err := time.Parse("2006-01-02", dateStr)
			if err != nil {
				continue
			}
			if t.Before(cutoff) {
				os.Remove(path)
			}
		}
	}
}
