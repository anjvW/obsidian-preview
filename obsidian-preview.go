package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

type FileNode struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	IsDir    bool        `json:"isDir"`
	Children []*FileNode `json:"children,omitempty"`
}

var mdFiles []string
var fileTree *FileNode
var rootDir string
var mu sync.RWMutex

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Println("用法: obsidian-preview")
		fmt.Println("启动 HTTP 服务器在 9099 端口，自动监听文件变化")
		os.Exit(0)
	}

	rootDir = "."
	fmt.Printf("正在扫描目录: %s\n", rootDir)

	// 初始扫描
	err := rescanDirectory()
	if err != nil {
		log.Fatalf("扫描目录错误: %v\n", err)
	}

	// 生成初始 HTML
	err = generateHTML("index.html")
	if err != nil {
		log.Fatalf("生成 HTML 错误: %v\n", err)
	}

	fmt.Printf("找到 %d 个 markdown 文件\n", len(mdFiles))

	// 启动文件监听
	go watchFiles()

	// 启动 HTTP 服务器（简单的静态文件服务）
	http.Handle("/", http.FileServer(http.Dir(".")))

	fmt.Printf("HTTP 服务器启动在 http://localhost:9099\n")
	fmt.Printf("按 Ctrl+C 停止服务器\n")
	log.Fatal(http.ListenAndServe(":9099", nil))
}

func rescanDirectory() error {
	mu.Lock()
	defer mu.Unlock()

	mdFiles = []string{}
	fileTree = &FileNode{Name: ".", Path: ".", IsDir: true}
	return scanDirectory(rootDir, fileTree)
}

func scanDirectory(dir string, parent *FileNode) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	// 排序：目录在前，然后按名称排序
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		name := entry.Name()

		// 跳过隐藏文件和目录
		if strings.HasPrefix(name, ".") && name != "." {
			continue
		}

		// 跳过 node_modules 等常见目录
		if entry.IsDir() && (name == "node_modules" || name == ".git") {
			continue
		}

		path := filepath.Join(dir, name)
		if dir == "." {
			path = name
		}

		node := &FileNode{
			Name:  name,
			Path:  path,
			IsDir: entry.IsDir(),
		}

		if entry.IsDir() {
			err := scanDirectory(path, node)
			if err != nil {
				continue
			}
			if len(node.Children) > 0 {
				parent.Children = append(parent.Children, node)
			}
		} else if strings.HasSuffix(strings.ToLower(name), ".md") {
			mdFiles = append(mdFiles, path)
			parent.Children = append(parent.Children, node)
		}
	}

	return nil
}

func watchFiles() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("创建文件监听器错误: %v\n", err)
		return
	}
	defer watcher.Close()

	// 递归添加所有目录到监听器
	err = filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			// 跳过隐藏目录
			if strings.HasPrefix(filepath.Base(path), ".") && filepath.Base(path) != "." {
				return filepath.SkipDir
			}
			// 跳过 node_modules 等
			if filepath.Base(path) == "node_modules" || filepath.Base(path) == ".git" {
				return filepath.SkipDir
			}
			return watcher.Add(path)
		}
		return nil
	})

	if err != nil {
		log.Printf("添加监听路径错误: %v\n", err)
		return
	}

	// 防抖：避免频繁更新
	var debounceTimer *time.Timer
	debounceDelay := 500 * time.Millisecond

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// 只处理 markdown 文件的变化
			if strings.HasSuffix(strings.ToLower(event.Name), ".md") ||
				event.Op&fsnotify.Create != 0 ||
				event.Op&fsnotify.Remove != 0 ||
				event.Op&fsnotify.Rename != 0 {
				// 重置防抖定时器
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.AfterFunc(debounceDelay, func() {
					fmt.Printf("检测到文件变化，重新扫描...\n")
					err := rescanDirectory()
					if err != nil {
						log.Printf("重新扫描错误: %v\n", err)
						return
					}
					err = generateHTML("index.html")
					if err != nil {
						log.Printf("重新生成 HTML 错误: %v\n", err)
						return
					}
					fmt.Printf("已更新，找到 %d 个 markdown 文件\n", len(mdFiles))
				})
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("文件监听错误: %v\n", err)
		}
	}
}

// 读取并渲染 markdown 文件
func renderMarkdownFile(filePath string) (string, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	// 使用 goldmark 渲染 markdown
	var buf bytes.Buffer
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			html.WithHardWraps(),
			html.WithXHTML(),
		),
	)

	if err := md.Convert(content, &buf); err != nil {
		return "", err
	}

	// 处理图片路径
	htmlContent := fixImagePaths(buf.String(), filePath)

	// 处理 Mermaid 代码块
	htmlContent = processMermaidBlocks(htmlContent)

	return htmlContent, nil
}

// 修复 markdown 中的图片路径
func fixImagePaths(htmlContent, mdFilePath string) string {
	// 获取 markdown 文件所在目录（相对于根目录）
	mdDir := filepath.Dir(mdFilePath)
	if mdDir == "." {
		mdDir = ""
	}

	// 使用更安全的方式处理图片标签
	var result strings.Builder
	content := htmlContent
	processed := 0
	maxIterations := 1000

	for processed < maxIterations {
		start := strings.Index(content, `<img src="`)
		if start == -1 {
			result.WriteString(content)
			break
		}

		result.WriteString(content[:start])

		start += len(`<img src="`)
		end := strings.Index(content[start:], `"`)
		if end == -1 {
			result.WriteString(content[start-len(`<img src="`):])
			break
		}

		imgPath := content[start : start+end]
		tagEnd := strings.Index(content[start+end:], `>`)
		if tagEnd == -1 {
			result.WriteString(content[start-len(`<img src="`):])
			break
		}

		originalImgTag := content[start-len(`<img src="`) : start+end+tagEnd+1]

		// 检查是否已经处理过
		if strings.Contains(originalImgTag, `onclick="openImageModal`) {
			result.WriteString(originalImgTag)
			content = content[start+end+tagEnd+1:]
			continue
		}

		// 处理相对路径
		if !strings.HasPrefix(imgPath, "/") && !strings.HasPrefix(imgPath, "http://") && !strings.HasPrefix(imgPath, "https://") && !strings.HasPrefix(imgPath, "data:") {
			var fullPath string
			if strings.HasPrefix(imgPath, "../") || strings.HasPrefix(imgPath, "./") {
				fullPath = filepath.Join(mdDir, imgPath)
			} else if mdDir != "" {
				fullPath = filepath.Join(mdDir, imgPath)
			} else {
				fullPath = imgPath
			}

			fullPath = filepath.Clean(fullPath)
			fullPath = strings.ReplaceAll(fullPath, "\\", "/")
			if strings.HasPrefix(fullPath, "/") {
				fullPath = fullPath[1:]
			}

			// 转换为相对路径（用于静态文件服务）
			newTag := strings.Replace(originalImgTag, `src="`+imgPath+`"`, `src="`+fullPath+`" class="preview-image" onclick="openImageModal(this.src)"`, 1)
			result.WriteString(newTag)
		} else {
			beforeClose := originalImgTag[:len(originalImgTag)-1]
			newTag := beforeClose + ` class="preview-image" onclick="openImageModal(this.src)">`
			result.WriteString(newTag)
		}

		content = content[start+end+tagEnd+1:]
		processed++
	}

	if processed >= maxIterations {
		return htmlContent
	}

	return result.String()
}

// 处理 Mermaid 代码块
func processMermaidBlocks(htmlContent string) string {
	content := htmlContent

	// 匹配 <pre><code class="language-mermaid">...</code></pre>
	for {
		start := strings.Index(content, `<pre><code class="language-mermaid">`)
		if start == -1 {
			// 也尝试匹配不带 language- 的
			start = strings.Index(content, `<pre><code class="mermaid">`)
			if start == -1 {
				break
			}
		}

		// 找到代码块的结束位置
		endTag := `</code></pre>`
		end := strings.Index(content[start:], endTag)
		if end == -1 {
			break
		}

		end += start + len(endTag)

		// 提取代码内容
		codeStart := start + len(`<pre><code class="language-mermaid">`)
		if strings.Contains(content[start:codeStart], `class="mermaid"`) {
			codeStart = start + len(`<pre><code class="mermaid">`)
		}
		codeContent := content[codeStart : end-len(endTag)]

		// 清理代码内容（移除 HTML 实体）
		codeContent = strings.ReplaceAll(codeContent, "&lt;", "<")
		codeContent = strings.ReplaceAll(codeContent, "&gt;", ">")
		codeContent = strings.ReplaceAll(codeContent, "&amp;", "&")
		codeContent = strings.TrimSpace(codeContent)

		// 替换为 Mermaid div
		mermaidDiv := `<div class="mermaid">` + codeContent + `</div>`
		content = content[:start] + mermaidDiv + content[end:]
	}

	return content
}

func generateHTML(outputFile string) error {
	mu.RLock()
	treeJSON, err := json.Marshal(fileTree.Children)
	mu.RUnlock()
	if err != nil {
		return err
	}

	// 读取并渲染所有 markdown 文件
	filesData := make(map[string]string)
	total := len(mdFiles)
	for i, filePath := range mdFiles {
		if (i+1)%10 == 0 || i == 0 {
			fmt.Printf("正在处理文件 %d/%d: %s\n", i+1, total, filePath)
		}

		htmlContent, err := renderMarkdownFile(filePath)
		if err != nil {
			filesData[filePath] = fmt.Sprintf("<p>渲染错误: %v</p>", err)
			continue
		}
		filesData[filePath] = htmlContent
	}
	fmt.Printf("文件处理完成，正在生成 HTML...\n")

	// 将文件数据转换为 JSON
	filesJSON, err := json.Marshal(filesData)
	if err != nil {
		return err
	}

	// 生成 HTML
	tmpl := `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Obsidian 笔记预览</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }

        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
            background: #1e1e1e;
            color: #d4d4d4;
            display: flex;
            height: 100vh;
            overflow: hidden;
        }

        .sidebar {
            width: 300px;
            background: #252526;
            border-right: 1px solid #3e3e42;
            display: flex;
            flex-direction: column;
            overflow: hidden;
        }

        .sidebar-header {
            padding: 15px;
            background: #2d2d30;
            border-bottom: 1px solid #3e3e42;
        }

        .sidebar-header h1 {
            font-size: 18px;
            color: #ffffff;
            margin-bottom: 10px;
        }

        .search-box {
            width: 100%;
            padding: 8px 12px;
            background: #3c3c3c;
            border: 1px solid #3e3e42;
            border-radius: 4px;
            color: #d4d4d4;
            font-size: 14px;
        }

        .search-box:focus {
            outline: none;
            border-color: #007acc;
        }

        .file-tree {
            flex: 1;
            overflow-y: auto;
            padding: 10px;
        }

        .file-tree::-webkit-scrollbar {
            width: 8px;
        }

        .file-tree::-webkit-scrollbar-track {
            background: #1e1e1e;
        }

        .file-tree::-webkit-scrollbar-thumb {
            background: #424242;
            border-radius: 4px;
        }

        .file-tree::-webkit-scrollbar-thumb:hover {
            background: #4e4e4e;
        }

        .tree-item {
            padding: 4px 8px;
            cursor: pointer;
            border-radius: 3px;
            user-select: none;
            display: flex;
            align-items: center;
            font-size: 14px;
        }

        .tree-item:hover {
            background: #2a2d2e;
        }

        .tree-item.active {
            background: #37373d;
            color: #ffffff;
        }

        .tree-item.folder {
            font-weight: 500;
            color: #4ec9b0;
        }

        .tree-item.file {
            color: #9cdcfe;
        }

        .tree-item-icon {
            margin-right: 6px;
            font-size: 12px;
            width: 16px;
            text-align: center;
            cursor: pointer;
        }

        .tree-item-icon.expandable {
            cursor: pointer;
        }

        .tree-children {
            display: block;
        }

        .tree-children.collapsed {
            display: none;
        }

        .content-area {
            flex: 1;
            display: flex;
            flex-direction: column;
            overflow: hidden;
        }

        .content-header {
            padding: 15px 20px;
            background: #2d2d30;
            border-bottom: 1px solid #3e3e42;
        }

        .content-header h2 {
            font-size: 16px;
            color: #ffffff;
        }

        .content-body {
            flex: 1;
            overflow-y: auto;
            padding: 30px;
            background: #1e1e1e;
        }

        .content-body::-webkit-scrollbar {
            width: 12px;
        }

        .content-body::-webkit-scrollbar-track {
            background: #1e1e1e;
        }

        .content-body::-webkit-scrollbar-thumb {
            background: #424242;
            border-radius: 6px;
        }

        .content-body::-webkit-scrollbar-thumb:hover {
            background: #4e4e4e;
        }

        .markdown-body {
            max-width: 900px;
            margin: 0 auto;
            line-height: 1.6;
        }

        .markdown-body h1,
        .markdown-body h2,
        .markdown-body h3,
        .markdown-body h4,
        .markdown-body h5,
        .markdown-body h6 {
            margin-top: 24px;
            margin-bottom: 16px;
            font-weight: 600;
            line-height: 1.25;
            color: #ffffff;
        }

        .markdown-body h1 {
            font-size: 2em;
            border-bottom: 1px solid #3e3e42;
            padding-bottom: 10px;
        }

        .markdown-body h2 {
            font-size: 1.5em;
            border-bottom: 1px solid #3e3e42;
            padding-bottom: 8px;
        }

        .markdown-body h3 {
            font-size: 1.25em;
        }

        .markdown-body p {
            margin-bottom: 16px;
            color: #d4d4d4;
        }

        .markdown-body code {
            background: #2d2d30;
            padding: 2px 6px;
            border-radius: 3px;
            font-family: "Consolas", "Monaco", "Courier New", monospace;
            font-size: 0.9em;
            color: #d7ba7d;
        }

        .markdown-body pre {
            background: #252526;
            border: 1px solid #3e3e42;
            border-radius: 6px;
            padding: 16px;
            overflow-x: auto;
            margin-bottom: 16px;
            position: relative;
        }

        .markdown-body pre code {
            background: transparent;
            padding: 0;
            color: #d4d4d4;
            font-size: 14px;
            line-height: 1.45;
            display: block;
        }

        /* 代码块复制按钮 */
        .code-block-wrapper {
            position: relative;
            margin-bottom: 16px;
        }

        .code-block-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            background: #2d2d30;
            border: 1px solid #3e3e42;
            border-bottom: none;
            border-radius: 6px 6px 0 0;
            padding: 8px 12px;
            font-size: 12px;
            color: #858585;
        }

        .code-block-header .language {
            font-weight: 500;
            color: #4ec9b0;
        }

        .copy-button {
            background: #3c3c3c;
            border: 1px solid #3e3e42;
            color: #d4d4d4;
            padding: 4px 12px;
            border-radius: 4px;
            cursor: pointer;
            font-size: 12px;
            transition: all 0.2s;
        }

        .copy-button:hover {
            background: #4c4c4c;
            border-color: #007acc;
        }

        .copy-button.copied {
            background: #007acc;
            color: #ffffff;
        }

        .code-block-wrapper pre {
            margin: 0;
            border-radius: 0 0 6px 6px;
        }

        .markdown-body ul,
        .markdown-body ol {
            margin-bottom: 16px;
            padding-left: 30px;
            color: #d4d4d4;
        }

        .markdown-body li {
            margin-bottom: 8px;
        }

        .markdown-body blockquote {
            border-left: 4px solid #007acc;
            padding-left: 16px;
            margin: 16px 0;
            color: #858585;
        }

        .markdown-body table {
            border-collapse: collapse;
            margin-bottom: 16px;
            width: 100%;
        }

        .markdown-body table th,
        .markdown-body table td {
            border: 1px solid #3e3e42;
            padding: 8px 12px;
            text-align: left;
        }

        .markdown-body table th {
            background: #2d2d30;
            font-weight: 600;
            color: #ffffff;
        }

        .markdown-body table tr:nth-child(even) {
            background: #252526;
        }

        .markdown-body a {
            color: #4ec9b0;
            text-decoration: none;
        }

        .markdown-body a:hover {
            text-decoration: underline;
        }

        .markdown-body img {
            max-width: 100%;
            height: auto;
            border-radius: 4px;
            margin: 16px 0;
            cursor: pointer;
            transition: opacity 0.2s;
        }

        .markdown-body img:hover {
            opacity: 0.8;
        }

        .preview-image {
            cursor: zoom-in;
        }

        /* 图片预览模态框 */
        .image-modal {
            display: none;
            position: fixed;
            z-index: 1000;
            left: 0;
            top: 0;
            width: 100%;
            height: 100%;
            background-color: rgba(0, 0, 0, 0.9);
            cursor: zoom-out;
        }

        .image-modal.active {
            display: flex;
            align-items: center;
            justify-content: center;
        }

        .image-modal img {
            max-width: 90%;
            max-height: 90%;
            object-fit: contain;
            border-radius: 8px;
        }

        .image-modal-close {
            position: absolute;
            top: 20px;
            right: 30px;
            color: #ffffff;
            font-size: 40px;
            font-weight: bold;
            cursor: pointer;
            z-index: 1001;
        }

        .image-modal-close:hover {
            color: #4ec9b0;
        }

        .empty-state {
            text-align: center;
            padding: 60px 20px;
            color: #858585;
        }

        .empty-state h3 {
            font-size: 20px;
            margin-bottom: 10px;
            color: #d4d4d4;
        }

        .hidden {
            display: none;
        }

        /* Mermaid 图表样式 */
        .mermaid {
            text-align: center;
            margin: 20px 0;
            background: #252526;
            border: 1px solid #3e3e42;
            border-radius: 6px;
            padding: 20px;
        }
    </style>
    <script src="https://cdnjs.cloudflare.com/ajax/libs/mermaid/11.12.0/mermaid.min.js"></script>
</head>
<body>
    <div class="sidebar">
        <div class="sidebar-header">
            <h1>📚 笔记库</h1>
            <input type="text" class="search-box" id="searchBox" placeholder="搜索文件...">
        </div>
        <div class="file-tree" id="fileTree"></div>
    </div>
    <div class="content-area">
        <div class="content-header">
            <h2 id="currentFile">选择一个文件</h2>
        </div>
        <div class="content-body">
            <div class="empty-state" id="emptyState">
                <h3>👈 从左侧选择文件</h3>
                <p>选择一个 markdown 文件开始预览</p>
            </div>
            <div class="markdown-body hidden" id="markdownContent"></div>
        </div>
    </div>

    <!-- 图片预览模态框 -->
    <div class="image-modal" id="imageModal" onclick="closeImageModal()">
        <span class="image-modal-close" onclick="closeImageModal()">&times;</span>
        <img id="modalImage" src="" alt="预览图片">
    </div>

    <script>
        const fileTreeData = {{.TreeJSON}};
        const filesData = {{.FilesJSON}};

        function renderTree(nodes, container, level = 0, parentItem = null) {
            nodes.forEach(node => {
                const item = document.createElement('div');
                item.className = 'tree-item' + (node.isDir ? ' folder' : ' file');
                item.style.paddingLeft = (level * 16 + 8) + 'px';
                
                const icon = document.createElement('span');
                icon.className = 'tree-item-icon';
                
                if (node.isDir && node.children && node.children.length > 0) {
                    icon.textContent = '▶';
                    icon.classList.add('expandable');
                    icon.style.transform = 'rotate(0deg)';
                    icon.style.transition = 'transform 0.2s';
                    icon.dataset.expanded = 'false';
                    
                    icon.addEventListener('click', (e) => {
                        e.stopPropagation();
                        const expanded = icon.dataset.expanded === 'true';
                        const childrenContainer = item.nextElementSibling;
                        
                        if (expanded) {
                            icon.dataset.expanded = 'false';
                            icon.style.transform = 'rotate(0deg)';
                            if (childrenContainer) {
                                childrenContainer.classList.add('collapsed');
                            }
                        } else {
                            icon.dataset.expanded = 'true';
                            icon.style.transform = 'rotate(90deg)';
                            if (childrenContainer) {
                                childrenContainer.classList.remove('collapsed');
                            }
                        }
                    });
                } else if (node.isDir) {
                    icon.textContent = '📁';
                } else {
                    icon.textContent = '📄';
                }
                
                const name = document.createElement('span');
                name.textContent = node.name;
                
                item.appendChild(icon);
                item.appendChild(name);
                
                if (!node.isDir) {
                    item.addEventListener('click', () => {
                        document.querySelectorAll('.tree-item').forEach(el => {
                            el.classList.remove('active');
                        });
                        item.classList.add('active');
                        showFile(node.path);
                    });
                } else {
                    item.addEventListener('click', (e) => {
                        if (e.target === icon) return;
                        const expandIcon = item.querySelector('.expandable');
                        if (expandIcon) {
                            expandIcon.click();
                        }
                    });
                }
                
                container.appendChild(item);
                
                if (node.isDir && node.children && node.children.length > 0) {
                    const childrenContainer = document.createElement('div');
                    childrenContainer.className = 'tree-children collapsed';
                    container.appendChild(childrenContainer);
                    renderTree(node.children, childrenContainer, level + 1, item);
                }
            });
        }

        function showFile(path) {
            const contentDiv = document.getElementById('markdownContent');
            const emptyState = document.getElementById('emptyState');
            const currentFile = document.getElementById('currentFile');
            
            const content = filesData[path];
            
            if (content) {
                contentDiv.innerHTML = content;
                
                // 处理代码块：添加复制按钮
                processCodeBlocks(contentDiv);
                
                // 初始化 Mermaid 图表
                if (typeof mermaid !== 'undefined') {
                    mermaid.initialize({ 
                        startOnLoad: true,
                        theme: 'dark',
                        themeVariables: {
                            primaryColor: '#007acc',
                            primaryTextColor: '#d4d4d4',
                            primaryBorderColor: '#3e3e42',
                            lineColor: '#4ec9b0',
                            secondaryColor: '#252526',
                            tertiaryColor: '#1e1e1e'
                        }
                    });
                    mermaid.run();
                }
                
                contentDiv.classList.remove('hidden');
                emptyState.classList.add('hidden');
                currentFile.textContent = path;
            } else {
                contentDiv.classList.add('hidden');
                emptyState.classList.remove('hidden');
                currentFile.textContent = '文件未找到';
            }
        }

        // 处理代码块：添加复制按钮
        function processCodeBlocks(container) {
            const preElements = container.querySelectorAll('pre code');
            
            preElements.forEach(preCode => {
                const pre = preCode.parentElement;
                if (pre.classList.contains('processed')) {
                    return; // 已经处理过
                }
                pre.classList.add('processed');
                
                // 跳过 Mermaid 代码块（已经处理过）
                if (preCode.className.includes('mermaid')) {
                    return;
                }
                
                // 获取语言类型
                let language = 'text';
                const classList = preCode.className.split(' ');
                for (const cls of classList) {
                    if (cls.startsWith('language-')) {
                        language = cls.replace('language-', '');
                        break;
                    }
                }
                const code = preCode.textContent;
                
                // 创建包装器
                const wrapper = document.createElement('div');
                wrapper.className = 'code-block-wrapper';
                
                // 创建头部（语言和复制按钮）
                const header = document.createElement('div');
                header.className = 'code-block-header';
                const langSpan = document.createElement('span');
                langSpan.className = 'language';
                langSpan.textContent = language;
                const copyBtn = document.createElement('button');
                copyBtn.className = 'copy-button';
                copyBtn.textContent = '复制';
                copyBtn.onclick = function() { copyCode(this); };
                copyBtn.dataset.code = code;
                header.appendChild(langSpan);
                header.appendChild(copyBtn);
                
                // 包装 pre 元素
                const newPre = document.createElement('pre');
                newPre.appendChild(preCode.cloneNode(true));
                
                wrapper.appendChild(header);
                wrapper.appendChild(newPre);
                
                // 替换原来的 pre
                pre.parentNode.replaceChild(wrapper, pre);
            });
        }

        // 复制代码功能
        function copyCode(button) {
            const code = button.dataset.code;
            navigator.clipboard.writeText(code).then(() => {
                const originalText = button.textContent;
                button.textContent = '已复制!';
                button.classList.add('copied');
                setTimeout(() => {
                    button.textContent = originalText;
                    button.classList.remove('copied');
                }, 2000);
            }).catch(err => {
                console.error('复制失败:', err);
                alert('复制失败，请手动选择复制');
            });
        }

        // 图片预览功能
        function openImageModal(src) {
            const modal = document.getElementById('imageModal');
            const modalImg = document.getElementById('modalImage');
            modalImg.src = src;
            modal.classList.add('active');
        }

        function closeImageModal() {
            const modal = document.getElementById('imageModal');
            modal.classList.remove('active');
        }

        document.addEventListener('keydown', (e) => {
            if (e.key === 'Escape') {
                closeImageModal();
            }
        });

        // 搜索功能
        document.getElementById('searchBox').addEventListener('input', (e) => {
            const searchTerm = e.target.value.toLowerCase();
            const items = document.querySelectorAll('.tree-item');
            
            items.forEach(item => {
                const text = item.textContent.toLowerCase();
                if (text.includes(searchTerm)) {
                    item.classList.remove('hidden');
                    let parent = item.parentElement;
                    while (parent && parent.classList.contains('tree-children')) {
                        parent.classList.remove('collapsed');
                        const prevSibling = parent.previousElementSibling;
                        if (prevSibling) {
                            const expandIcon = prevSibling.querySelector('.expandable');
                            if (expandIcon) {
                                expandIcon.dataset.expanded = 'true';
                                expandIcon.style.transform = 'rotate(90deg)';
                            }
                        }
                        parent = parent.parentElement;
                    }
                } else {
                    item.classList.add('hidden');
                }
            });
        });

        // 初始化
        const treeContainer = document.getElementById('fileTree');
        renderTree(fileTreeData, treeContainer);
    </script>
</body>
</html>`

	t, err := template.New("html").Parse(tmpl)
	if err != nil {
		return err
	}

	file, err := os.Create(outputFile)
	if err != nil {
		return err
	}
	defer file.Close()

	data := struct {
		TreeJSON  template.JS
		FilesJSON template.JS
	}{
		TreeJSON:  template.JS(string(treeJSON)),
		FilesJSON: template.JS(string(filesJSON)),
	}

	return t.Execute(file, data)
}
