export function page(token: string): string {
  return `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="publisher-token" content="` + token + `">
  <meta name="color-scheme" content="light">
  <title>应用发布 · 本机</title>
  <link rel="stylesheet" href="/app.css">
  <script defer src="/app.js"></script>
</head>
<body>
  <a class="skip-link" href="#main">跳转到内容</a>
  <header class="topbar">
    <a class="brand" href="#/" aria-label="应用发布首页">
      <span class="brand-symbol" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M12 3 3.5 7.5v9L12 21l8.5-4.5v-9L12 3Zm0 0v9m-8.5-4.5L12 12l8.5-4.5M12 12v9"/></svg></span>
      <span>Android Publisher</span>
    </a>
    <span class="header-slash">/</span>
    <span class="header-label">应用发布</span>
    <span class="local-status"><span></span> 本机运行</span>
  </header>
  <div class="workspace">
    <aside class="sidebar" aria-label="主导航">
      <div class="workspace-label">应用</div>
      <nav id="navigation"></nav>
      <div class="sidebar-bottom"><span class="tiny-dot"></span> 草稿自动保存在本机<br><span class="muted">CLI 与 Web 共用发布结果</span></div>
    </aside>
    <main id="main" tabindex="-1"><div class="loading-state"><span class="spinner"></span> 正在读取本机应用…</div></main>
  </div>
  <div id="toast" role="status" aria-live="polite" hidden></div>
  <div id="modal"></div>
</body>
</html>`;
}
