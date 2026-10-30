# Sticky note（便签）

一款轻量、精美的多用户笔记应用，面向飞牛 NAS（fnOS）家庭/团队场景。支持富文本编辑、全文搜索、笔记加密、两步验证（TOTP）和多用户数据隔离，可部署为 fnOS FPK 应用、独立二进制或 Windows 桌面客户端。

当前版本：**2.3.1**（文件夹加密 / 访问锁 + 双端契约一致性修复：锁定文件夹在全部出口隐藏、按会话解锁、登出/关页即回锁；桌面端导出提示、排序、锁定夹 423 引导与网页端对齐）

## 功能特性

- **富文本编辑**：基于 Tiptap 的所见即所得编辑，支持标题、加粗、斜体、下划线、删除线、引用、代码块、列表、文字颜色、图片、表格
- **全文搜索**：SQLite FTS5 全文索引，短词自动回退 LIKE
- **笔记加密**：AES-256-GCM + PBKDF2（600,000 次迭代）+ bcrypt，加密笔记不以明文落库
- **两步验证**：TOTP 动态码 + 恢复码
- **并发编辑保护**：CAS 乐观锁，冲突时返回 409
- **附件生命周期**：staged → attached → orphan → delete_pending 状态机，魔数校验拒绝伪装文件
- **回收站**：软删除可恢复，永久删除级联清理附件
- **Markdown 导入导出**：单篇 + 批量 ZIP
- **多用户**：数据完全隔离，首个注册用户自动成为管理员
- **PWA / 暗色模式 / 响应式**：桌面与移动端自适应
- **Windows 桌面客户端**：Wails 构建的离线客户端，支持本地优先与同步

## 技术栈

- **前端**：Vue 3 + Element Plus + Tiptap + Vite 8 + Pinia
- **后端**：Go + Gin + SQLite（modernc.org/sqlite，纯 Go，无 CGO，单二进制）
- **认证**：JWT 双 token（access 2h / refresh 7–30 天）+ bcrypt + token_version 全局吊销
- **安全**：bluemonday 双端 XSS 消毒、CSP、防爆破三层（IP / 用户名 / 账号）、外链图片剥除
- **桌面端**：Wails v2 + Vue 3 + 本地 SQLite

## 快速开始

### 直接运行二进制

```bash
./sticky-note
# 访问 http://localhost:13389
# 第一个注册的用户自动成为管理员
```

### 飞牛 NAS（fnOS）

1. 下载 `StickyNote-<日期>-<版本>.fpk`（当前仅 x86/amd64）
2. 飞牛应用中心 → 手动安装 → 上传 FPK
3. 按向导配置端口（默认 `13389`）；JWT 密钥默认自动生成并持久化

### 从源码构建

```bash
# 前置：Go 1.26.10+、Node.js 22+
make build          # 当前平台
make cross-compile  # Linux amd64/arm64、macOS amd64/arm64
make build-fpk      # fnOS FPK 包
```

构建产物输出到 `release/<version>/`。

## 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | `13389` | 服务端口 |
| `DATA_DIR` | `./data` | 数据目录；SQLite 数据库与主密钥由此派生 |
| `UPLOAD_DIR` | `$DATA_DIR/upload` | 附件存储目录 |
| `JWT_SECRET` | 随机生成 | JWT 签名密钥；未设置时自动生成并持久化到 `$DATA_DIR/.jwt_secret` |
| `COOKIE_SECURE` | 自动 | 强制 Cookie Secure 标志；默认跟随 HTTPS |

> 生产环境建议通过 fnOS 反向代理启用 HTTPS，并设置 `COOKIE_SECURE=1`。

## 桌面客户端

Windows 桌面客户端位于 `desktop/`，使用 Wails v2 构建。构建说明见 [desktop/README.md](desktop/README.md)。

## 项目结构

```text
├── server/              # Go 后端
│   ├── auth/            # JWT 双 token
│   ├── handler/         # API 处理器
│   ├── model/           # 数据模型
│   ├── middleware/      # 认证 / CORS / 限流
│   ├── crypto/          # AES-256-GCM + PBKDF2
│   ├── database/        # SQLite + 迁移
│   └── static/          # 嵌入前端资源
├── web/                 # Vue 3 前端
├── desktop/             # Wails 桌面客户端
├── fnpack/              # fnOS FPK 模板
├── scripts/             # 构建、测试与质量门禁脚本
├── docs/                # 公开文档
├── VERSION              # 版本号唯一事实源
└── Makefile             # 构建入口
```

## 测试与质量门禁

- Go 单元测试（含 race 检测）
- 前端 Vitest + ESLint
- 冒烟测试与 Playwright E2E
- `govulncheck` / `npm audit` 基线差分
- `scripts/check_secrets.py` 凭据扫描
- VERSION 与 `fnpack/manifest` 一致性校验

## 安全

发现安全问题请通过 GitHub Security Advisories 私下报告，不要公开创建 Issue。详见 [SECURITY.md](SECURITY.md)。

## License

MIT，详见 [LICENSE](LICENSE)。
