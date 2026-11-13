# CI 运行手册

本文是 CI 红灯、Release 失败、依赖告警和测试 flaky 的处置 SOP。目标不是让 CI “变绿”，而是让每次失败可分类、可定位、可防复发。

## 1. 原则

1. **本地预检优先**：能本地发现的漂移，不进入 CI。
2. **失败不静默**：工具故障、扫描协议不完整、非空但无效输出一律 fail-closed。
3. **红灯必须分类**：禁止只写“CI 挂了”。
4. **重跑不是修复**：环境抖动最多重跑一次；连续两红必须查根因。
5. **不加自动重试到绿**：不让重试机制掩盖真实问题。
6. **依赖告警走基线**：必须有原因、修复版本和 DUE 日期，不允许临时关闭检查。

## 2. 本地预检

push 前至少执行：

```bash
python scripts/ci_preflight.py --quick
```

完整预检（含 SBOM 与凭据扫描）：

```bash
python scripts/ci_preflight.py
```

公共导出根存在时：

```bash
python scripts/ci_preflight.py --public-root <public-repo>
```

## 3. 红灯分类矩阵

| 类型 | 典型症状 | 第一处置 |
|---|---|---|
| 元数据漂移 | `VERSION` 与 `fnpack/manifest`、`wails.json`、公共 overlay 不一致 | 跑 `ci_preflight.py --quick`；修正后重跑 |
| 依赖告警 | govulncheck/npm audit 新增 advisory | 核实影响面；能升级则升级；否则 baseline + DUE |
| 测试 race / TempDir | Linux race、目录删除失败、随机失败 | 查并发收尾顺序；不得放宽断言 |
| 构建资产/缓存 | WebView2、runtime、dist 哈希不一致 | 查缓存 key、钉版清单、构建脚本是否双源 |
| 工具链/协议 | govulncheck 找不到 go、扫描输出不完整 | 先修工具可用性与协议校验，再看漏洞 |
| 网络/发布面 | push、Release、Docker 上传连接重置 | 最多重试 2 次；仍失败转人工 |
| 配额/权限 | artifact/storage quota、token 权限 | 降低保留期/体积；检查权限最小化 |

## 4. 红灯处置步骤

1. 记录五项：仓库、run id、commit、失败 job、失败 step。
2. 拉取最后 100~200 行日志，不凭猜测改代码。
3. 判断是否可本地复现：
   - 可复现 → 本地修到绿，再推。
   - 不可复现 → 重跑一次；仍红按真实红灯处理。
4. 分类后写入 `docs/CI事件台账.md`（如未建立，则在版本记录中登记）。
5. 防复发措施必须落到脚本、workflow、文档或预检，不允许只写“已修复”。
6. CI 全绿后再进入 Release/tag。

## 5. Release / Docker 专项

- tag 只用 `vX.Y.Z`；`-R` 预发布版本不得触发正式 Docker tag。
- 发布前检查：
  ```bash
  python scripts/ci_preflight.py --quick
  python scripts/release_publish.py --dry-run --repo <owner>/<repo> --tag v2.6.6 --assets <file>...
  ```
- Release 资产名使用 ASCII，避免 GitHub API 对非 ASCII 名称做归一。
- Docker 正式 tag 发布前，同 commit 必须有成功 CI。
- 私有仓 Docker workflow 保持 disabled，避免与公开仓竞争同一镜像标签。

## 6. 测试稳定性守则

1. `Init` 类函数返回后不得启动可能写文件的 goroutine；必要预热同步执行。
2. 测试收尾顺序：关库 → 关文件 → 停 goroutine → 让 `t.TempDir()` 清理。
3. 并发测试使用条件等待，不使用固定 sleep。
4. race 相关测试本地至少 `-count=3`。
5. TempDir 清理失败不是小事，通常是句柄或后台任务泄漏。
6. 不允许通过放宽断言消除 flaky；预算放大必须配合反向验证。

## 7. 防复发台账

每个 CI 红灯必须回答：

- 根因类型是什么？
- 为什么本地预检没拦住？
- 是否需要新增预检项、workflow 断言或文档规则？
- 反向验证怎么做？

不能回答这四问的，不得关闭事件。
