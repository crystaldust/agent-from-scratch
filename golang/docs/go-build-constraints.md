# Go 构建约束：让参考实现留在章节目录里，但不参与构建

> 本项目每个章节目录里放两类文件：
>
> - `main.go`：自己写的代码，`go run .` 运行的就是它
> - `xx-xxx.byclaude.go`：AI 生成的参考实现，第一行是 `//go:build ignore`，平时不参与构建，需要时单独运行
>
> 两个文件都是 `package main`、都有 `func main()`，却不会冲突。本文讲清楚这背后的机制：Go 怎么决定编译哪些文件、`//go:build` 怎么写、为什么 `ignore` 能起作用，以及常见的坑。
>
> 文中的输出都是在 Go 1.27.3（macOS）上实际运行得到的。

---

## 目录

1. [现象：两个 `package main`，却没有报错](#1-现象两个-package-main却没有报错)
2. [Go 怎么决定编译哪些文件](#2-go-怎么决定编译哪些文件)
3. [`//go:build` 构建约束](#3-gobuild-构建约束)
4. [`ignore` 不是关键字，只是约定](#4-ignore-不是关键字只是约定)
5. [两种运行方式：按目录、按文件](#5-两种运行方式按目录按文件)
6. [影响编译的文件名和目录名规则](#6-影响编译的文件名和目录名规则)
7. [本项目的用法速查](#7-本项目的用法速查)
8. [和 Python 的对比](#8-和-python-的对比)
9. [常见坑](#9-常见坑)

---

## 1. 现象：两个 `package main`，却没有报错

```
golang/01-agent-loop/
├── main.go                    ← package main，有 func main()
└── 01-agentloop.byclaude.go   ← package main，也有 func main()
```

在 `golang/` 下执行 `go run ./01-agent-loop/`，运行的总是 `main.go`，也不报错。

一个常见的误解是"Go 先找到 `main.go`，然后忽略其他文件"。**不是这样的**：Go 编译时不看文件名，`main.go` 这个名字没有任何特殊含义。真正起作用的是参考实现的第一行：

```go
//go:build ignore
```

---

## 2. Go 怎么决定编译哪些文件

Go 的规则是：**一个目录就是一个包**。`go run ./01-agent-loop/` 的意思是"编译并运行这个目录对应的包"，Go 会把目录里**所有**满足条件的 `.go` 文件放在一起编译，不分先后、不分主次。

如果把参考实现的 `//go:build ignore` 删掉，两个文件都会参与编译，两个 `func main()` 就冲突了：

```
# example.com/bt/ch
ch/ref.go:5:6: main redeclared in this block
	ch/main.go:5:6: other declaration of main
```

（这是用一个最小示例复现的输出。真实项目里还会同时报 `model`、`bashTool` 等常量和变量重复声明。）

可以用 `go list` 直接看 Go 选了哪些文件：

```bash
cd golang
go list -f '{{.GoFiles}} {{.IgnoredGoFiles}}' ./01-agent-loop/
# [main.go] [01-agentloop.byclaude.go]
```

- `GoFiles`：参与编译的文件
- `IgnoredGoFiles`：因为构建约束被排除的文件

---

## 3. `//go:build` 构建约束

### 3.1 写法

构建约束（build constraint）是一行特殊注释，后面跟一个条件表达式。**条件成立时，这个文件才参与编译**：

```go
//go:build linux               // 只在 Linux 上编译
//go:build darwin && arm64     // 只在 Apple Silicon 的 macOS 上编译
//go:build linux || darwin     // Linux 或 macOS
//go:build !windows            // 除了 Windows 都编译
//go:build go1.22              // Go 1.22 及以上版本才编译
//go:build ignore              // 永远不编译（见第 4 节）
```

表达式支持 `&&`、`||`、`!` 和括号，和 Go 的布尔表达式一样。

### 3.2 位置要求

```go
//go:build ignore
                                  ← 惯例空一行（gofmt 会自动补上）
// 参考实现：和 main.go 同目录……   ← 这之后才是普通注释
package main
```

- 必须写在 `package` 子句**之前**，前面只能有空行和其他注释
- `//` 和 `go:build` 之间**不能有空格**。`// go:build ignore` 只是一行普通注释，约束不生效：

  ```
  $ go list -f '{{.GoFiles}} {{.IgnoredGoFiles}}' ./ch/   # ref.go 第一行写成 // go:build ignore
  [main.go ref.go] []
  ```

- 后面的空行**不是必需的**：实测不空行，约束也照样生效。不过 `gofmt` 会自动在约束后面插入一个空行，所以保存后总会变成空一行的样子。网上常说"必须空一行"，指的是旧的 `// +build` 语法（见第 3.4 节），那种写法不空行就会被当成包文档注释，约束失效

### 3.3 标签从哪里来

表达式里的每个名字都叫"标签"（tag），一个标签是否成立，取决于以下来源：

| 来源 | 例子 |
|---|---|
| 当前操作系统（`GOOS`） | `linux`、`darwin`、`windows` |
| 当前 CPU 架构（`GOARCH`） | `amd64`、`arm64` |
| Go 版本 | `go1.21`：Go 1.21 及以上版本都成立 |
| 是否启用 cgo | `cgo` |
| 命令行手动指定 | `go build -tags integration` 让 `integration` 成立 |

没出现在上述来源里的名字，一律当作不成立。

### 3.4 旧语法 `// +build`

老代码里可能看到这样的写法：

```go
// +build linux,amd64
```

这是 Go 1.17 之前的语法，逗号表示"且"，空格表示"或"，很容易写错。Go 1.17 起改用 `//go:build`，现在只需要写新语法。

---

## 4. `ignore` 不是关键字，只是约定

`ignore` 并没有被 Go 特殊对待。第 3.3 节里没有任何来源会让 `ignore` 成立，所以 `//go:build ignore` 的条件永远为假，文件永远被排除。

写成 `//go:build nobodyuses` 效果也一样。大家都写 `ignore` 是因为它表达了意图，而且工具也认这个约定（见第 7.3 节 gopls）。官方文档 `go help buildconstraint` 里也是这样推荐的。

### 坑：不要用 `-tags ignore` 让它"重新生效"

你可能会想：既然 `ignore` 是个普通标签，那 `go run -tags ignore .` 不就能把参考实现也编译进来吗？实测会**整个构建崩溃**：

```
.../src/internal/goos/...: found packages main (gengoos.go) and goos (goos.go) in .../src/internal/goos
.../src/runtime/...:      found packages runtime (abi_test.go) and main (mkduff.go) in .../src/runtime
...
imports text/template from gen_sort_variants.go: import cycle not allowed
```

原因是 `-tags` 对**所有包**生效，包括标准库。标准库里也有大量用 `//go:build ignore` 排除的代码生成脚本（`gengoos.go`、`mkduff.go`……），它们一下子全被编译进来，于是到处都是包名冲突和循环导入。

这也从侧面说明了 `ignore` 这个约定在 Go 生态里有多普遍。

---

## 5. 两种运行方式：按目录、按文件

`go run`、`go build`、`go vet` 都接受两种参数，筛选文件的方式不同：

| 命令 | 编译哪些文件 | 构建约束 |
|---|---|---|
| `go run .` 或 `go run ./01-agent-loop/` | 目录里的所有 `.go` 文件 | **生效**，排除 `ignore` 文件 |
| `go run 01-agentloop.byclaude.go` | **只编译**命令行上列出的文件 | 不再按约束筛选 |

所以参考实现可以这样单独运行：

```bash
cd golang/01-agent-loop
go run 01-agentloop.byclaude.go
```

反过来，**把两个文件同时列出来**就会冲突，因为列出的文件都会被编译：

```
$ go run ./ch/main.go ./ch/ref.go
# command-line-arguments
ch/ref.go:7:6: main redeclared in this block
	ch/main.go:5:6: other declaration of main
```

按文件编译时，包名显示为 `command-line-arguments`，这是 Go 给"由命令行文件列表组成的临时包"起的名字。

这种用法在 Go 社区很常见：代码生成脚本通常写成 `//go:build ignore` 加 `package main`，平时不参与项目编译，需要时用 `go run gen.go` 单独运行，或者通过 `//go:generate go run gen.go` 调用。

---

## 6. 影响编译的文件名和目录名规则

除了 `//go:build`，下面这些命名也会影响一个文件是否参与编译：

| 命名 | 效果 |
|---|---|
| `xxx_test.go` | 只在 `go test` 时编译 |
| `xxx_linux.go`、`xxx_arm64.go`、`xxx_darwin_arm64.go` | 根据后缀自动加上对应的系统/架构约束，相当于隐式的 `//go:build` |
| 以 `_` 或 `.` 开头的文件，如 `_draft.go` | 直接忽略，连 `IgnoredGoFiles` 里都不会列出 |
| 名为 `testdata` 的目录，以及以 `_` 或 `.` 开头的目录 | `./...` 通配时跳过 |

注意文件名后缀的坑：如果把文件命名为 `notes_linux.go`，在 macOS 上它就不会被编译，而且不会有任何提示。

`01-agentloop.byclaude.go` 的名字不属于以上任何一种，所以它被排除完全是因为第一行的约束。

---

## 7. 本项目的用法速查

### 7.1 约定

- 每章目录里，`main.go`（以及以后可能拆出来的其他文件）是自己写的代码
- 参考实现命名为 `xx-xxx.byclaude.go`，**第一行固定为 `//go:build ignore`**
- 参考实现必须是**单个文件、自包含**：它不参与目录包的编译，所以不能调用 `main.go` 里的函数，`main.go` 也不能调用它

### 7.2 命令

在章节目录（如 `golang/01-agent-loop`）下：

| 目的 | 命令 |
|---|---|
| 运行自己的代码 | `go run .` |
| 运行参考实现 | `go run 01-agentloop.byclaude.go` |
| 检查自己的代码 | `go vet .` |
| 检查参考实现 | `go vet 01-agentloop.byclaude.go` |
| 查看哪些文件参与编译 | `go list -f '{{.GoFiles}} {{.IgnoredGoFiles}}' .` |

要特别注意：`go vet ./...`、`go build ./...`、`go test ./...` 都**不会检查参考实现**。参考实现即使编译不通过，这些命令也照样通过。要检查参考实现，必须像上表那样单独指定文件名。

`gofmt` 不受影响：它按文件格式化代码，不关心构建约束。

### 7.3 编辑器（VS Code + gopls）

gopls 有一个设置 `build.standaloneTags`，默认值就是 `["ignore"]`。带 `//go:build ignore` 的 `package main` 文件会被当作"独立文件"处理，自成一个包，所以在 VS Code 里打开参考实现，跳转定义、自动补全、错误提示都能正常使用，不需要额外配置。

gopls 只认**单个标签**这种最简单的形式。根据 `gopls api-json` 里这个设置的说明，写成 `//go:build ignore && go1.22` 这样的组合条件就不算独立文件了。

如果以后把参考实现的标签换成别的名字（比如 `//go:build reference`），就要在设置里把它加进 `build.standaloneTags`，否则 gopls 会提示这个文件不属于任何包。

### 7.4 调试

`.vscode/launch.json` 里的 "Debug current chapter" 配置用的是 `"program": "${fileDirname}"`，即按**目录**调试，所以按 F5 调试的总是 `main.go`，参考实现不会参与。要调试参考实现，可以临时把 `program` 改成 `"${file}"`，然后在打开参考实现的情况下按 F5。

---

## 8. 和 Python 的对比

| | Python | Go |
|---|---|---|
| 编译/运行的单位 | 单个文件（模块） | 一个目录（包） |
| 同目录下多个"入口" | 每个文件都可以有 `if __name__ == "__main__":`，互不影响 | 同一个包里只能有一个 `func main()` |
| 让某个文件不参与 | 不 import 它就行 | 需要构建约束，或者放到别的目录 |
| 单独运行某个文件 | `python ref.py` | `go run ref.go`（文件需自包含） |

Python 里"同目录放两个入口"是天然的，Go 里则需要借助构建约束。这也是 Go"包 = 目录"设计带来的差别：目录里的文件默认就是一个整体。

---

## 9. 常见坑

| 坑 | 现象 | 修正 |
|---|---|---|
| 以为 Go 只编译 `main.go` | 去掉约束后出现 `main redeclared in this block` | Go 编译目录里所有满足约束的文件，文件名不重要 |
| 写成 `// go:build ignore`（多了空格） | 约束不生效，出现重复声明错误 | `//` 和 `go:build` 之间不能有空格 |
| 用旧语法 `// +build`，后面又没有空行 | 约束不生效 | 改用 `//go:build`；`gofmt` 会自动补空行 |
| 用 `go run -tags ignore .` 想把参考实现也编译进来 | 标准库报包名冲突和循环导入，整个构建崩溃 | 不要设置 `ignore` 标签；要运行参考实现就单独指定文件名 |
| `go run main.go 01-agentloop.byclaude.go` | `main redeclared` | 按文件运行时，列出的文件都会编译；一次只列一个 |
| 以为 `go vet ./...` 检查了参考实现 | 参考实现有问题，却没有任何报错 | 用 `go vet 01-agentloop.byclaude.go` 单独检查 |
| 参考实现调用了 `main.go` 里的函数 | 单独运行时报 `undefined` | 参考实现保持单文件、自包含 |
| 文件名恰好以 `_linux.go` 等结尾 | 在其他系统上该文件被悄悄排除 | 避免用 `_系统名`、`_架构名` 作为文件名后缀 |
