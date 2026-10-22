// Command snboot-testserver —— 批次 5 桌面 Bootstrap 集成测试的**进程外**服务端工装。
// 存在理由：桌面测试与 syncsrv 同进程会共用 database.DB 全局（句柄乒乓产生假通过路径），
// 真 HTTP + 独立进程才是可信集成面。仅供 go test 构建拉起，不进交付。
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"

	"stickynote/app"
	authpkg "stickynote/auth"
	"stickynote/database"
	"stickynote/model"
	syncsrv "stickynote/sync"
)

func main() {
	dataDir := flag.String("data", "", "data dir")
	addr := flag.String("addr", "127.0.0.1:0", "监听地址（固定地址供断网-回归用例重启复用同一 URL）")
	flag.Parse()
	if *dataDir == "" {
		log.Fatal("-data required")
	}
	if err := database.Init(filepath.Join(*dataDir, "srv.db"), "99.0.0"); err != nil {
		log.Fatal(err)
	}
	// 幂等播种：断网-回归用例会**同数据目录重启**本进程，非 OR IGNORE 会撞 users.id UNIQUE 直接 fatal。
	if _, err := database.DB.Exec(`INSERT OR IGNORE INTO users (id, username, password_hash, role) VALUES (1,'u1','x','user')`); err != nil {
		log.Fatal(err)
	}
	syncsrv.Configure(*dataDir)
	upload := filepath.Join(*dataDir, "upload")
	if err := os.MkdirAll(upload, 0o750); err != nil {
		log.Fatal(err)
	}
	syncsrv.SetAttachmentsDir(upload)
	syncsrv.ConfigureSnapshot(filepath.Join(*dataDir, "snap"), upload)

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	// 测试身份注入（等价 RequireAuth 的下游状态；凭据面不在本工装射程）。
	auth := r.Group("/api", func(c *gin.Context) {
		c.Set("userID", int64(1))
		c.Set("username", "u1")
		c.Set("role", "user")
		c.Next()
	})
	auth.POST("/sync/push", syncsrv.Push)
	auth.GET("/sync/pull", syncsrv.Pull)
	auth.GET("/sync/state", syncsrv.State)
	auth.POST("/sync/snapshot", syncsrv.SnapshotCreate)
	auth.GET("/sync/snapshot/:id", syncsrv.SnapshotStatus)
	auth.GET("/sync/snapshot/:id/download", syncsrv.SnapshotDownload)
	// 测试驱动面：播种笔记 / 模拟恢复换代。
	auth.POST("/test/seed", func(c *gin.Context) {
		title := c.Query("title")
		web := app.NewServerNoteService(100000)
		n, err := web.CreateNote(1, title, "<p>s</p>", "", nil)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"id": n.ID})
	})
	auth.POST("/test/reset-epoch", func(c *gin.Context) {
		cur, err := model.GetServerEpoch()
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		if err := model.SetServerEpoch(cur + 1); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"epoch": cur + 1})
	})
	auth.GET("/test/token", func(c *gin.Context) {
		tok, err := authpkg.GenerateAccessToken(1, "u1", "user", 0)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"access_token": tok})
	})
	auth.GET("/test/has-note", func(c *gin.Context) {
		var n int
		database.DB.QueryRow(`SELECT COUNT(*) FROM notes WHERE title=?`, c.Query("title")).Scan(&n)
		c.JSON(200, gin.H{"count": n})
	})

	// 固定地址供"断网→同址回归"用例复用同一 URL；刚 kill 完端口可能暂未释放，故有界重试。
	var ln net.Listener
	var err error
	for i := 0; i < 50; i++ {
		ln, err = net.Listen("tcp", *addr)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		log.Fatalf("listen %s: %v", *addr, err)
	}
	fmt.Printf("ADDR http://%s\n", ln.Addr().String())
	log.Fatal(r.RunListener(ln))
}
