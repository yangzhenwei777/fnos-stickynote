package main

// identity_heal.go —— v21.152 启动自愈：代际切换后身份档案缺席时从上一代找回。
//
// 背景（生产实锤）：快照激活切代时 client_profile 由 bootstrap.CarryClientIdentity 随代迁移；
// 一旦该步未落（真机观测：新代库无该表、而旧代档案完好），用户侧表现是——
// 「账号与设备」显示未登录、同步/重建同步全部失败（NO_REFRESH），且用户无任何可操作出路
// （只能重登，重登后又可能在新一轮换代中再次丢失）。
//
// 本自愈在每次启动时做一次最小修补：当代库**确无身份行**时，按 mtime 从新到旧扫描其它代际库，
// 找到第一份含身份行的档案整行迁回。幂等、只增不改、失败仅记日志（绝不阻断启动）。

import (
	"database/sql"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"stickynote-desktop/bootstrap"
	"stickynote-desktop/localauth"
	"stickynote/database"
)

// healIdentityAcrossGenerations 见文件头注释。profileDir = 本机档案根（localDataDir）。
func healIdentityAcrossGenerations(profileDir string) {
	p, err := localauth.Load(database.DB)
	if err == nil && p.ServerURL != "" {
		return // 当代身份在场：正常路径，什么都不做
	}
	if err != nil && !errors.Is(err, localauth.ErrNoProfile) &&
		!strings.Contains(err.Error(), "no such table") {
		log.Printf("[auth] identity heal skipped (load err): %v", err)
		return
	}
	gensDir := filepath.Join(profileDir, "generations")
	ents, derr := os.ReadDir(gensDir)
	if derr != nil {
		return
	}
	type cand struct {
		path string
		mod  time.Time
	}
	var cands []cand
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		dbPath := filepath.Join(gensDir, e.Name(), "notes.db")
		fi, serr := os.Stat(dbPath)
		if serr != nil {
			continue
		}
		cands = append(cands, cand{path: dbPath, mod: fi.ModTime()})
	}
	// 新→旧扫描（快照新代可能没有身份；有身份的那代通常是上一代或更早）
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })
	for _, c := range cands {
		old, oerr := sql.Open("sqlite", "file:"+c.path+"?mode=ro")
		if oerr != nil {
			continue
		}
		var n int
		qerr := old.QueryRow(`SELECT COUNT(*) FROM client_profile WHERE id=1`).Scan(&n)
		if qerr != nil || n == 0 {
			old.Close()
			continue
		}
		if cerr := bootstrap.CarryClientIdentity(old, database.DB); cerr != nil {
			log.Printf("[auth] identity heal carry failed (%s): %v", c.path, cerr)
			old.Close()
			continue
		}
		old.Close()
		if p2, lerr := localauth.Load(database.DB); lerr == nil && p2.ServerURL != "" {
			log.Printf("[auth] identity healed from %s (device=%s server=%s)",
				filepath.Base(filepath.Dir(c.path)), p2.DeviceID, p2.ServerURL)
		}
		return
	}
}
