const basePath = window.location.pathname.startsWith("/temu/") ? "/temu/" : "/";
const apiUrl = (path) => `${basePath}api/${path}`;
const healthUrl = `${basePath}healthz`;

const elements = {
  sidebar: document.querySelector("#sidebar"),
  backdrop: document.querySelector("#sidebar-backdrop"),
  input: document.querySelector("#file-input"),
  choose: document.querySelector("#choose-button"),
  dropzone: document.querySelector("#dropzone"),
  progress: document.querySelector("#upload-progress"),
  progressName: document.querySelector("#progress-name"),
  progressPercent: document.querySelector("#progress-percent"),
  progressBar: document.querySelector("#progress-bar"),
  message: document.querySelector("#message"),
  empty: document.querySelector("#empty-state"),
  table: document.querySelector("#table-wrap"),
  list: document.querySelector("#upload-list"),
  refresh: document.querySelector("#refresh-button"),
};

const escapeHtml = (value) => String(value).replace(/[&<>'"]/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" })[char]);
const fileSize = (bytes) => bytes < 1024 * 1024 ? `${(bytes / 1024).toFixed(1)} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`;
const formatTime = (value) => new Intl.DateTimeFormat("zh-CN", { dateStyle: "short", timeStyle: "short", hour12: false }).format(new Date(value));

function setServiceState(online) {
  for (const id of ["sidebar-dot", "heading-dot"]) {
    const dot = document.querySelector(`#${id}`);
    dot.classList.remove("checking", "offline");
    if (!online) dot.classList.add("offline");
  }
  document.querySelector("#sidebar-state").textContent = online ? "文件服务正常" : "文件服务异常";
  document.querySelector("#heading-state").textContent = online ? "后端服务已连接" : "后端服务未连接";
}

async function checkHealth() {
  try {
    const response = await fetch(healthUrl, { cache: "no-store" });
    setServiceState(response.ok);
  } catch (_) {
    setServiceState(false);
  }
}

function showMessage(text, error = false) {
  elements.message.textContent = text;
  elements.message.classList.toggle("error", error);
  elements.message.hidden = false;
}

function renderUploads(items) {
  elements.empty.hidden = items.length > 0;
  elements.table.hidden = items.length === 0;
  elements.list.innerHTML = items.map((item) => {
    const sheets = item.sheets?.map((sheet) => sheet.name).join("、") || "-";
    const rows = item.sheets?.reduce((sum, sheet) => sum + sheet.rows, 0) || 0;
    const columns = Math.max(0, ...(item.sheets?.map((sheet) => sheet.columns) || []));
    return `<tr>
      <td><div class="file-name"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 2h9l4 4v16H6z"/><path d="M14 2v5h5"/><path d="M9 13h6M9 17h6"/></svg><strong title="${escapeHtml(item.original_name)}">${escapeHtml(item.original_name)}</strong></div></td>
      <td class="sheet-list" title="${escapeHtml(sheets)}">${escapeHtml(item.sheet_count)} 个 · ${escapeHtml(sheets)}</td>
      <td>${rows.toLocaleString("zh-CN")} 行 · 最多 ${columns} 列</td>
      <td>${fileSize(item.size)}</td>
      <td>${formatTime(item.uploaded_at)}</td>
      <td><span class="ready-badge">已读取</span></td>
    </tr>`;
  }).join("");
}

async function loadUploads() {
  elements.refresh.classList.add("loading");
  try {
    const response = await fetch(apiUrl("uploads"), { cache: "no-store" });
    const payload = await response.json();
    if (!response.ok || !payload.success) throw new Error(payload.error || "无法读取文件列表");
    renderUploads(payload.data);
  } catch (error) {
    showMessage(error.message || "无法读取文件列表", true);
  } finally {
    elements.refresh.classList.remove("loading");
  }
}

function validateFile(file) {
  if (!file.name.toLowerCase().endsWith(".xlsx")) return "仅支持 .xlsx 格式的 Excel 文件";
  if (file.size > 50 * 1024 * 1024) return "文件大小不能超过 50 MB";
  if (file.size === 0) return "不能上传空文件";
  return "";
}

function uploadFile(file) {
  const validationError = validateFile(file);
  if (validationError) return showMessage(validationError, true);

  elements.message.hidden = true;
  elements.progress.hidden = false;
  elements.progressName.textContent = file.name;
  elements.choose.disabled = true;
  const request = new XMLHttpRequest();
  request.open("POST", apiUrl("uploads"));
  request.upload.onprogress = (event) => {
    if (!event.lengthComputable) return;
    const percent = Math.round((event.loaded / event.total) * 100);
    elements.progressPercent.textContent = `${percent}%`;
    elements.progressBar.style.width = `${percent}%`;
  };
  request.onload = async () => {
    elements.choose.disabled = false;
    elements.progress.hidden = true;
    elements.input.value = "";
    let payload;
    try { payload = JSON.parse(request.responseText); } catch (_) { payload = {}; }
    if (request.status >= 200 && request.status < 300 && payload.success) {
      showMessage(`已读取 ${payload.data.sheet_count} 个工作表，文件正在等待分析规则。`);
      await loadUploads();
    } else {
      showMessage(payload.error || "上传失败，请稍后重试", true);
    }
  };
  request.onerror = () => {
    elements.choose.disabled = false;
    elements.progress.hidden = true;
    showMessage("网络连接失败，请检查文件服务", true);
  };
  const formData = new FormData();
  formData.append("file", file);
  request.send(formData);
}

elements.choose.addEventListener("click", (event) => { event.stopPropagation(); elements.input.click(); });
elements.dropzone.addEventListener("click", () => elements.input.click());
elements.dropzone.addEventListener("keydown", (event) => { if (event.key === "Enter" || event.key === " ") elements.input.click(); });
elements.input.addEventListener("change", () => { if (elements.input.files[0]) uploadFile(elements.input.files[0]); });
for (const eventName of ["dragenter", "dragover"]) elements.dropzone.addEventListener(eventName, (event) => { event.preventDefault(); elements.dropzone.classList.add("dragging"); });
for (const eventName of ["dragleave", "drop"]) elements.dropzone.addEventListener(eventName, (event) => { event.preventDefault(); elements.dropzone.classList.remove("dragging"); });
elements.dropzone.addEventListener("drop", (event) => { if (event.dataTransfer.files[0]) uploadFile(event.dataTransfer.files[0]); });
elements.refresh.addEventListener("click", loadUploads);
document.querySelector("#mobile-menu").addEventListener("click", () => { elements.sidebar.classList.add("sidebar-open"); elements.backdrop.classList.add("visible"); });
for (const selector of ["#sidebar-close", "#sidebar-backdrop"]) document.querySelector(selector).addEventListener("click", () => { elements.sidebar.classList.remove("sidebar-open"); elements.backdrop.classList.remove("visible"); });

void checkHealth();
void loadUploads();
