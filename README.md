# Obsidian 笔记预览工具

一个用 Go 语言编写的 CLI 工具，用于生成 Obsidian 笔记库的 HTML 预览页面，支持在浏览器中美观地预览 markdown 文件。
生成index.html文件后可复制库到web服务器中作为网页使用。

## 功能特性

- 📁 **文件树浏览**：左侧显示完整的文件树结构，支持文件夹折叠/展开
- 🔍 **文件搜索**：实时搜索文件，自动展开匹配项的父文件夹
- 📝 **Markdown 渲染**：使用 Goldmark 渲染 markdown，支持 GFM 语法
- 🖼️ **图片预览**：点击图片可放大预览，支持 ESC 键关闭
- 📋 **代码块复制**：代码块显示语言类型和复制按钮，一键复制代码
- 📊 **Mermaid 图表**：支持 Mermaid 图表渲染（包括甘特图、流程图等）
- 🔄 **自动更新**：监听文件变化，自动重新生成 HTML
- 🎨 **深色主题**：美观的深色主题界面

## 安装

### 前置要求

- Go 1.21 或更高版本

### 安装依赖

```bash
go mod download
```

### 编译

```bash
go build -o obsidian-preview obsidian-preview.go
```

或者直接运行：

```bash
go run obsidian-preview.go
```

## 使用方法

### 基本使用

1. 在 Obsidian 库的根目录执行程序：

```bash
./obsidian-preview
```

或者：

```bash
go run obsidian-preview.go
```

2. 程序会：
   - 扫描当前目录下的所有 `.md` 文件
   - 生成 `index.html` 文件
   - 启动 HTTP 服务器在 `http://0.0.0.0:9099`

3. 在浏览器中打开 9099端口 即可预览笔记

### 查看帮助

```bash
./obsidian-preview -h
# 或
./obsidian-preview --help
```

## 功能说明

### 文件树

- 左侧显示完整的文件目录结构
- 点击文件夹图标或名称可以展开/折叠文件夹
- 点击文件可以预览内容
- 支持搜索功能，输入关键词即可过滤文件

## 文件监听

使用本程序会自动监听文件变化：
- 当 markdown 文件被创建、修改或删除时
- 程序会自动重新扫描目录
- 并重新生成 `index.html` 文件
- 刷新浏览器即可看到更新

## 技术栈

- **Go 1.21+**：主要编程语言
- **Goldmark**：Markdown 渲染引擎
- **fsnotify**：文件系统监听
- **Mermaid.js**：图表渲染（通过 CDN）

## 项目结构

```
.
├── go.mod               # Go 模块定义
├── go.sum               # 依赖校验和
├── index.html           # 生成的预览页面（运行后生成）
└── README.md            # 本文件
```

## 注意事项

1. 程序会在当前目录生成 `index.html` 文件
2. HTTP 服务器默认监听 9099 端口
3. 程序会跳过隐藏文件和目录（以 `.` 开头，除了 `.` 本身）
4. 程序会跳过 `node_modules` 和 `.git` 目录
5. 图片路径支持相对路径，会自动转换为正确的路径

## 常见问题

### Q: 图片无法显示？

A: 确保图片路径正确，程序会自动处理相对路径。如果使用 HTTP 服务器访问，图片路径应该是相对于根目录的。

### Q: Mermaid 图表不显示？

A: 确保网络可以访问 Cloudflare CDN，程序使用 `https://cdnjs.cloudflare.com/ajax/libs/mermaid/11.12.0/mermaid.min.js` 加载 Mermaid 库。

### Q: 如何停止服务器？

A: 在终端中按 `Ctrl+C` 停止服务器。

### Q: 文件变化后没有自动更新？

A: 程序会在检测到文件变化后自动重新生成 HTML，刷新浏览器即可看到更新。如果长时间没有更新，可以手动重启程序。

## 许可证

本项目采用 MIT 许可证。
