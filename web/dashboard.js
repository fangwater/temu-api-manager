const pagePath = window.location.pathname.replace(/\/+$/, "");
const basePath = pagePath.endsWith(".html") ? pagePath.slice(0, pagePath.lastIndexOf("/")) : pagePath;
const apiPath = (path) => `${basePath}/api${path}`;
const defaultStore = { code: "panda-homes", name: "PANDA HOMES", default: true };
const requestedShopCode = new URLSearchParams(window.location.search).get("shop");
const savedShopCode = requestedShopCode || sessionStorage.getItem("temu_selected_shop") || defaultStore.code;
const state = { store: { ...defaultStore, code: savedShopCode }, shops: [], orders: [], orderMeta: {}, manualOrders: [], manualMeta: {}, history: [], historyMeta: {}, labelShipments: [], labelMeta: {}, exceptionShipments: [], exceptionMeta: {}, shipments: [], shipmentMeta: {}, pages: { orders: 1, manual: 1, labels: 1, exceptions: 1, ledger: 1, history: 1, skuRules: 1 }, pageSize: 30, warehouses: [], mappings: [], bulkBatch: null, currentOrder: null, recoveryShipment: null, warehousePreview: null, quote: null, quoteTimer: 0, quoteSequence: 0, quoteController: null, warehouseController: null, selectedChannelId: 0, operationKey: "" };
state.omsPlatformOrders = [];
state.omsPlatformOrderMeta = {};
state.omsPlatformOrderRequestSequence = 0;
state.omsPlatformOrderStatus = 0;
state.pages.omsStatuses = 1;
state.carrierPolicies = [];
state.skuWarehouseRules = [];
state.skuRuleMeta = {};
state.manualRequestSequence = 0;
state.combinedShipmentGroups = [];
state.combinedShipmentMeta = { total_groups: 0, total_orders: 0, queried_at: null, error: "" };
state.combinedShipmentRequestSequence = 0;
state.combinedShipmentLoading = false;
const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => [...document.querySelectorAll(selector)];
const escapeHtml = (value) => String(value ?? "").replace(/[&<>'"]/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" })[char]);
state.splitPlan = null;
state.splitQuote = null;
state.splitPlanRequestSequence = 0;
state.splitQuoteRequestSequence = 0;

const trackingStatusMappings = Object.freeze([
  { raw: "Last-Mile Manifest", zh: "待承运商揽收" },
  { raw: "Last Mile Carrier Pick up failed", zh: "揽收异常" },
  { raw: "Last Mile Carrier Picked up", zh: "已揽收" },
  { raw: "In transit", zh: "运输中" },
  { raw: "Arrived at Post Office", zh: "已到达投递网点" },
  { raw: "Out for Delivery", zh: "派送中" },
  { raw: "Delivery Exception", zh: "派送异常" },
  { raw: "Delivery Failure-Non carrier", zh: "非承运商原因派送失败" },
  { raw: "Delivered", zh: "已签收" },
  { raw: "Carrier accepted the claimed of Missing Parcel", zh: "承运商已受理丢件索赔" },
]);
const trackingStatusLabelByRaw = new Map(trackingStatusMappings.map(({ raw, zh }) => [raw, zh]));

function trackingStatusText(status) {
  const raw = String(status ?? "");
  return trackingStatusLabelByRaw.get(raw) || raw;
}

async function api(path, options = {}) {
  const { timeoutMs = 35000, sensitive = false, signal: externalSignal, ...fetchOptions } = options;
  const controller = new AbortController();
  let timedOut = false;
  const abortFromCaller = () => controller.abort();
  if (externalSignal?.aborted) controller.abort();
  else externalSignal?.addEventListener("abort", abortFromCaller, { once: true });
  const timer = setTimeout(() => { timedOut = true; controller.abort(); }, timeoutMs);
  const headers = { Accept: "application/json", "X-Temu-Shop": state.store.code, ...(fetchOptions.body ? { "Content-Type": "application/json" } : {}), ...(fetchOptions.headers || {}) };
  if (sensitive && state.operationKey) headers["X-Temu-Operation-Key"] = state.operationKey;
  try {
    const response = await fetch(apiPath(path), { cache: "no-store", ...fetchOptions, headers, signal: controller.signal });
    let payload = {};
    try { payload = await response.json(); } catch (_) { payload = {}; }
    if (!response.ok || !payload.success) {
      const error = new Error(payload.error || `请求失败 (${response.status})`);
      error.status = response.status;
      throw error;
    }
    return payload;
  } catch (error) {
    if (controller.signal.aborted) {
      const requestError = new Error(timedOut ? `请求超时（${Math.ceil(timeoutMs / 1000)}秒）` : "请求已取消");
      requestError.code = timedOut ? "REQUEST_TIMEOUT" : "REQUEST_ABORTED";
      throw requestError;
    }
    throw error;
  } finally {
    clearTimeout(timer);
    externalSignal?.removeEventListener("abort", abortFromCaller);
  }
}

function toast(message, error = false) {
  const element = $("#toast");
  element.textContent = message;
  element.classList.toggle("error", error);
  element.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => { element.hidden = true; }, 4200);
}

function setLoading(button, loading) {
  button.disabled = loading;
  button.classList.toggle("loading", loading);
}

function renderStoreIdentity() {
const name = state.store?.name || defaultStore.name;
document.title = `${name} · Temu Fulfillment`;
	const brand = document.querySelector(".brand strong");
	const subtitle = document.querySelector(".brand small");
	const crumb = document.querySelector(".crumb span");
	if (brand) brand.textContent = name;
	if (subtitle) subtitle.textContent = "Temu Fulfillment";
	if (crumb) crumb.textContent = name;
}

function operationKeyStorage() {
  return `temu_operation_key:${state.store.code}`;
}

function renderShopSelector() {
  const select = $("#shop-select");
  select.innerHTML = state.shops.map((shop) => `<option value="${escapeHtml(shop.code)}" ${shop.code === state.store.code ? "selected" : ""}>${escapeHtml(shop.name)}</option>`).join("");
}

async function loadShops() {
  let shops = [];
  let defaultShopCode = defaultStore.code;
  try {
    const { data } = await api("/system/shops");
    shops = data?.shops || [];
    defaultShopCode = data?.default_shop || defaultShopCode;
  } catch (_) {
    try {
      const { data } = await api("/system/store");
      shops = [data || defaultStore];
      defaultShopCode = shops[0].code;
    } catch (_) {
      shops = [defaultStore];
    }
  }
  state.shops = shops.length ? shops : [defaultStore];
  state.store = state.shops.find((shop) => shop.code === savedShopCode)
    || state.shops.find((shop) => shop.code === defaultShopCode)
    || state.shops[0];
  sessionStorage.setItem("temu_selected_shop", state.store.code);
  state.operationKey = sessionStorage.getItem(operationKeyStorage()) || "";
  renderShopSelector();
  renderStoreIdentity();
}

function formatTime(value) {
  if (!value) return "-";
  const date = typeof value === "number" ? new Date(value * 1000) : new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(date);
}

function elapsedDuration(value) {
  if (!value) return null;
  const timestamp = new Date(value).getTime();
  if (Number.isNaN(timestamp)) return null;
  const totalMinutes = Math.max(0, Math.floor((Date.now() - timestamp) / 60000));
  if (totalMinutes < 60) return `${totalMinutes} 分钟`;
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  return minutes === 0 ? `${hours} 小时` : `${hours} 小时 ${minutes} 分钟`;
}

function relativeDeadline(value) {
  if (!value) return "未返回";
  const seconds = value - Math.floor(Date.now() / 1000);
  if (seconds <= 0) return "已超时";
  const hours = Math.floor(seconds / 3600);
  return hours >= 24 ? `${Math.floor(hours / 24)} 天 ${hours % 24} 小时` : `${hours} 小时`;
}

function statusText(status) {
  return ({ submitting: "正在提交", label_pending: "面单生成中", label_ready: "面单就绪", label_failed: "购单失败", submission_unknown: "面单结果确认中", shipped: "已确认发货", confirm_failed: "确认失败" })[status] || status || "未发货";
}

function statusClass(status) {
  if (["label_failed", "submission_unknown", "confirm_failed"].includes(status)) return "failed";
  if (["submitting", "label_pending"].includes(status)) return "pending";
  if (!status) return "neutral";
  return "";
}

function renderTrackingStatusMappings(query = "") {
  const normalizedQuery = String(query).trim().toLocaleLowerCase();
  const items = trackingStatusMappings.filter(({ raw, zh }) => !normalizedQuery || `${raw} ${zh}`.toLocaleLowerCase().includes(normalizedQuery));
  $("#tracking-status-rows").innerHTML = items.map(({ raw }) => `<tr><td><code class="tracking-status-raw">${escapeHtml(raw)}</code></td><td><strong class="tracking-status-zh">${escapeHtml(trackingStatusText(raw))}</strong></td></tr>`).join("");
  $("#tracking-status-total").textContent = normalizedQuery ? `匹配 ${items.length} / ${trackingStatusMappings.length} 条` : `共 ${trackingStatusMappings.length} 条`;
  $("#tracking-status-empty").hidden = items.length > 0;
}

function renderPager(id, meta, key, loader) {
  const container = $(id);
  const total = Number(meta?.total || 0);
  const pageSize = Number(meta?.page_size || state.pageSize);
  const pageCount = Math.max(1, Math.ceil(total / pageSize));
  const page = Math.min(Math.max(1, Number(meta?.page || state.pages[key])), pageCount);
  state.pages[key] = page;
  container.hidden = total <= pageSize;
  container.innerHTML = `<button type="button" aria-label="上一页" title="上一页" ${page <= 1 ? "disabled" : ""}><svg viewBox="0 0 24 24"><path d="m15 18-6-6 6-6"/></svg></button><span>第 ${page} / ${pageCount} 页 · 共 ${total} 条</span><button type="button" class="next" aria-label="下一页" title="下一页" ${page >= pageCount ? "disabled" : ""}><svg viewBox="0 0 24 24"><path d="m15 18-6-6 6-6"/></svg></button>`;
  const buttons = container.querySelectorAll("button");
  buttons[0]?.addEventListener("click", () => { state.pages[key] = Math.max(1, page - 1); loader(); });
  buttons[1]?.addEventListener("click", () => { state.pages[key] = Math.min(pageCount, page + 1); loader(); });
}

function adjustEmptyPage(meta, key, loader) {
  const total = Number(meta?.total || 0);
  const pageSize = Number(meta?.page_size || state.pageSize);
  const lastPage = Math.max(1, Math.ceil(total / pageSize));
  if (state.pages[key] > lastPage) {
    state.pages[key] = lastPage;
    loader();
    return true;
  }
  return false;
}

async function checkHealth() {
  try {
    const response = await fetch(`${basePath}/healthz`, { cache: "no-store", headers: { "X-Temu-Shop": state.store.code } });
    $("#service-dot").className = response.ok ? "dot" : "dot error";
    $("#service-text").textContent = response.ok ? "Go 服务正常" : "服务异常";
  } catch (_) {
    $("#service-dot").className = "dot error";
    $("#service-text").textContent = "服务离线";
  }
}

async function loadToken() {
  const pill = $("#token-pill");
  try {
    const { data } = await api("/system/token-status");
    pill.className = `token-pill ${data.state === "healthy" ? "" : data.state}`;
    pill.querySelector(".dot").className = `dot ${data.state === "healthy" ? "" : data.state === "warning" ? "warning" : "error"}`;
    $("#token-text").textContent = data.valid ? `${data.remaining_text}后需授权` : "已过期，需要授权";
    pill.title = `到期时间：${new Date(data.expires_at).toLocaleString("zh-CN")}`;
  } catch (error) {
    pill.className = "token-pill error";
    pill.querySelector(".dot").className = "dot error";
    $("#token-text").textContent = "Token 检查失败";
  }
}

async function loadOrders() {
  const query = $("#order-search").value.trim();
  try {
    const payload = await api(`/orders?page=${state.pages.orders}&page_size=${state.pageSize}&queue=pending${query ? `&q=${encodeURIComponent(query)}` : ""}`);
    if (adjustEmptyPage(payload.meta, "orders", loadOrders)) return;
    state.orders = payload.data || [];
    state.orderMeta = payload.meta || {};
    renderOrders();
  } catch (error) { toast(error.message, true); }
}

function renderOrders() {
  const rows = $("#order-rows");
  const items = state.orders;
  rows.innerHTML = items.map((order) => {
    const lines = (order.lines || []).map((line) => `<span class="sku-line"><b>${escapeHtml(line.ext_code || "SKU待映射")}</b><span>× ${line.quantity}</span></span>`).join("");
    const review = order.manual_review;
    const manualBlocked = review?.active && (review.status !== "approved" || (review.reasons || []).some((reason) => ["sku_unbound", "inventory_rule"].includes(reason)));
    const classifications = (review?.reasons || []).map((reason) => `<span class="classification-tag">${manualReasonText(reason)}</span>`).join("");
    const job = order.auto_fulfillment;
    const running = job && !["failed", "completed", "skipped"].includes(job.status);
    const action = manualBlocked
      ? `<button class="row-action" data-open-manual="${escapeHtml(order.parent_order_sn)}">查看人工队列</button>`
      : `<button class="row-action" data-fulfill="${escapeHtml(order.parent_order_sn)}" ${running ? "disabled" : ""}>发货</button>`;
    const workflow = job ? ({ queued: "排队中", running: "自动发货中", waiting_label: "等待面单", confirming: "确认发货中", waiting_oms: "等待领星确认", completed: "已完成", skipped: "平台已处理", failed: "自动发货失败" })[job.status] || job.status : "待发货";
    const workflowTone = job?.status === "failed" ? "failed" : "pending";
    return `<tr>
      <td><div class="order-id"><strong>${escapeHtml(order.parent_order_sn)}</strong><small>更新 ${formatTime(order.update_time)}</small></div></td>
      <td><div class="sku-stack">${lines || "-"}</div>${classifications ? `<div class="classification-stack">${classifications}</div>` : ""}</td>
      <td><div class="order-id"><strong>${relativeDeadline(order.expect_ship_latest_time)}</strong><small>${formatTime(order.expect_ship_latest_time)}</small></div></td>
      <td>${escapeHtml(order.fulfillment_type || "fulfillBySeller")}</td>
      <td><span class="status-badge ${workflowTone}" title="${escapeHtml(job?.last_error || "")}">${escapeHtml(workflow)}</span></td>
      <td>${action}</td>
    </tr>`;
  }).join("");
  $("#orders-empty").hidden = items.length > 0;
  const units = items.reduce((sum, order) => sum + (order.lines || []).reduce((lineSum, line) => lineSum + line.quantity, 0), 0);
  $("#metric-orders").textContent = state.orderMeta.total ?? items.length;
  $("#metric-units").textContent = units;
  $("#metric-reserved").textContent = items.length;
  $("#metric-sync").textContent = state.orderMeta.sync?.completed_at ? formatTime(state.orderMeta.sync.completed_at) : "尚未同步";
  $("#nav-order-count").textContent = state.orderMeta.total ?? items.length;
  $("#order-total").textContent = `共 ${state.orderMeta.total ?? items.length} 条`;
  renderPager("#orders-pager", state.orderMeta, "orders", loadOrders);
  $$('[data-fulfill]').forEach((button) => button.addEventListener("click", () => openFulfillment(button.dataset.fulfill)));
  $$('[data-open-manual]').forEach((button) => button.addEventListener("click", () => { switchView("manual"); loadManualOrders(button.dataset.openManual); }));
}

function combinedOrderStatusText(status) {
  const normalized = Number(status);
  return normalized === 2 ? "待发货" : normalized ? `状态 ${normalized}` : "未知状态";
}

function combinedOrderStatusClass(status) {
  return Number(status) === 2 ? "pending" : "neutral";
}

function renderCombinedShipmentCandidates() {
  const groups = (state.combinedShipmentGroups || []).filter((group) => (group.orders || []).length);
  const meta = state.combinedShipmentMeta || {};
  const totalGroups = Number(meta.total_groups ?? groups.length);
  const totalOrders = Number(meta.total_orders ?? groups.reduce((sum, group) => sum + (group.orders || []).length, 0));
  $("#combined-rows").innerHTML = groups.map((group, groupIndex) => {
    const orders = group.orders || [];
    return orders.map((order, orderIndex) => `<tr class="${orderIndex === 0 ? "combined-group-start" : ""}">
      ${orderIndex === 0 ? `<td rowspan="${orders.length}" class="combined-group-cell"><div class="combined-group-label"><strong>候选组 ${groupIndex + 1}</strong><small>${orders.length} 个母订单</small></div></td>` : ""}
      <td><div class="order-id"><strong>${escapeHtml(order.parent_order_sn)}</strong><small>Temu 母订单</small></div></td>
      <td><span class="status-badge ${combinedOrderStatusClass(order.parent_order_status)}">${escapeHtml(combinedOrderStatusText(order.parent_order_status))}</span></td>
      <td>${formatTime(order.parent_order_time)}</td>
      <td><code class="combined-identifier">${escapeHtml(order.mall_id || "-")}</code></td>
      <td><code class="combined-identifier">${escapeHtml(order.semi_unique_id || "-")}</code></td>
    </tr>`).join("");
  }).join("");
  $("#metric-combined-groups").textContent = totalGroups;
  $("#metric-combined-orders").textContent = totalOrders;
  $("#metric-combined-shop").textContent = state.store.name || state.store.code;
  $("#metric-combined-query").textContent = meta.queried_at ? formatTime(meta.queried_at) : state.combinedShipmentLoading ? "查询中" : "尚未查询";
  $("#nav-combined-count").textContent = totalOrders;
  $("#combined-total").textContent = `共 ${totalGroups} 组 · ${totalOrders} 个母订单`;

  const empty = $("#combined-empty");
  empty.hidden = groups.length > 0;
  if (groups.length > 0) return;
  if (state.combinedShipmentLoading) {
    empty.querySelector("strong").textContent = "正在查询 Temu";
    empty.querySelector("span").textContent = "正在读取当前店铺的可合并发货候选组";
  } else if (meta.error) {
    empty.querySelector("strong").textContent = "查询失败";
    empty.querySelector("span").textContent = meta.error;
  } else {
    empty.querySelector("strong").textContent = "当前无可用的合并订单";
    empty.querySelector("span").textContent = "Temu 当前店铺未返回可合并发货候选组";
  }
}

async function loadCombinedShipmentCandidates() {
  const button = $("#refresh-combined");
  const requestSequence = ++state.combinedShipmentRequestSequence;
  state.combinedShipmentLoading = true;
  state.combinedShipmentGroups = [];
  state.combinedShipmentMeta = { total_groups: 0, total_orders: 0, queried_at: null, error: "" };
  setLoading(button, true);
  renderCombinedShipmentCandidates();
  try {
    const { data } = await api("/combined-shipment-candidates");
    if (requestSequence !== state.combinedShipmentRequestSequence) return;
    state.combinedShipmentGroups = data?.groups || [];
    state.combinedShipmentMeta = { ...(data || {}), error: "" };
  } catch (error) {
    if (requestSequence !== state.combinedShipmentRequestSequence) return;
    state.combinedShipmentGroups = [];
    state.combinedShipmentMeta = { total_groups: 0, total_orders: 0, queried_at: null, error: error.message };
    toast(error.message, true);
  } finally {
    if (requestSequence === state.combinedShipmentRequestSequence) {
      state.combinedShipmentLoading = false;
      setLoading(button, false);
      renderCombinedShipmentCandidates();
    }
  }
}

async function loadBulkFulfillment() {
  try {
    const { data } = await api("/auto-fulfillment/batches/latest");
    state.bulkBatch = data?.id ? data : null;
    renderBulkFulfillment();
  } catch (error) {
    console.error("load bulk fulfillment", error);
  }
}

function renderBulkFulfillment() {
  const batch = state.bulkBatch;
  const button = $("#bulk-fulfill");
  const restart = $("#restart-bulk");
  const text = $("#bulk-fulfill-text");
  const progress = $("#bulk-progress");
  progress.className = "bulk-progress";
  if (!batch) {
    restart.hidden = true;
    button.disabled = false;
    text.textContent = "一键发货";
    progress.hidden = true;
    return;
  }
  progress.hidden = false;
  progress.classList.add(batch.status);
  if (batch.status === "running") {
    restart.hidden = false;
    button.disabled = true;
    text.textContent = `发货中 ${batch.succeeded_orders}/${batch.total_orders}`;
    progress.textContent = `按时间顺序 · 最多 8 单并发 · 已完成 ${batch.succeeded_orders}/${batch.total_orders}${batch.current_order_sn ? ` · 当前队首 ${batch.current_order_sn}` : ""}`;
    return;
  }
  restart.hidden = batch.status === "completed";
  button.disabled = false;
  text.textContent = batch.status === "stopped" ? "重新一键发货" : "一键发货";
  progress.textContent = batch.status === "stopped"
    ? `已在 ${batch.failed_order_sn || "异常订单"} 停止 · ${batch.last_error || "自动发货失败"}`
    : `上一批已完成 ${batch.succeeded_orders}/${batch.total_orders}`;
}

async function restartBulkFulfillment() {
  if (!state.bulkBatch || state.bulkBatch.status === "completed") return;
  if (!window.confirm("重启当前批次？已有面单与发货账本会保留，仅重新调度未完成订单。")) return;
  const button = $("#restart-bulk");
  setLoading(button, true);
  try {
    const { data } = await api("/auto-fulfillment/batches/restart", { method: "POST", body: JSON.stringify({ confirm: true }) });
    state.bulkBatch = data;
    renderBulkFulfillment();
    toast("批次已安全重启，未完成订单已优先排队");
  } catch (error) {
    toast(error.message, true);
  } finally {
    setLoading(button, false);
  }
}

async function startBulkFulfillment() {
  if (state.bulkBatch?.status === "running") return;
  const total = Number(state.orderMeta.total ?? state.orders.length);
  if (total < 1) {
    toast("当前没有可自动发货的订单", true);
    return;
  }
  if (!window.confirm(`将按最晚发货时间依次处理当前 ${total} 个自动订单，最多 8 单并发；任一订单失败即停止派发新单。确认开始？`)) return;
  const button = $("#bulk-fulfill");
  setLoading(button, true);
  try {
    const { data } = await api("/auto-fulfillment/batches", { method: "POST", body: JSON.stringify({ confirm: true }) });
    state.bulkBatch = data.batch;
    renderBulkFulfillment();
    toast(data.created ? `批次已启动，共 ${data.batch.total_orders} 单` : `已有批次正在执行：${data.batch.succeeded_orders}/${data.batch.total_orders}`);
    await loadOrders();
  } catch (error) {
    toast(error.message, true);
  } finally {
    setLoading(button, false);
    renderBulkFulfillment();
  }
}

async function syncOrders() {
  const button = $("#sync-orders"); setLoading(button, true);
  try { const payload = await api("/orders/sync", { method: "POST" }); toast(`同步完成：${payload.data.fetched_orders} 个订单，${payload.data.fetched_lines} 个商品行`); await loadOrders(); }
  catch (error) { toast(error.message, true); }
  finally { setLoading(button, false); }
}
function manualReasonText(reason) {
  return ({ sku_unbound: "SKU 未绑定", inventory_rule: "库存安全线不足", warehouse_sku_spec_incomplete: "仓库 SKU 包裹数据缺失", delivery_address_unsupported: "偏远地址物流不支持", multi_item: "一单多件", merge_candidate: "合并候选", platform_consolidated: "Temu 已合并" })[reason] || reason;
  }

function manualStatusText(status) {
  return ({ detected: "待转人工", manual_pending: "人工处理中", approved: "已批准自动发货", resolved: "已结束" })[status] || status;
}

async function loadManualOrders(focusOrder = "") {
  const search = $("#manual-search");
  if (focusOrder) {
    state.pages.manual = 1;
    search.value = focusOrder;
  }
  const params = new URLSearchParams({ page: state.pages.manual, page_size: state.pageSize });
  const status = $("#manual-status").value;
  const query = search.value.trim();
  if (status) params.set("status", status);
  if (query) params.set("q", query);
  const requestSequence = ++state.manualRequestSequence;
  try {
    const payload = await api(`/manual-orders?${params}`);
    if (requestSequence !== state.manualRequestSequence) return;
    if (adjustEmptyPage(payload.meta, "manual", () => loadManualOrders(focusOrder))) return;
    state.manualOrders = payload.data || [];
    state.manualMeta = payload.meta || {};
    renderManualOrders(focusOrder);
  } catch (error) {
    if (requestSequence === state.manualRequestSequence) toast(error.message, true);
  }
}

async function exportManualOrders() {
  const button = $("#export-manual");
  setLoading(button, true);
  try {
    const params = new URLSearchParams();
    const status = $("#manual-status").value;
    const query = $("#manual-search").value.trim();
    if (status) params.set("status", status);
    if (query) params.set("q", query);
    const queryString = params.toString();
    const response = await fetch(apiPath(`/manual-orders/export.csv${queryString ? `?${queryString}` : ""}`), {
      cache: "no-store",
      headers: { "X-Temu-Shop": state.store.code },
    });
    if (!response.ok) {
      let message = `导出失败（${response.status}）`;
      try {
        const payload = await response.json();
        message = payload.error || message;
      } catch (_) {}
      throw new Error(message);
    }
    const blob = await response.blob();
    const disposition = response.headers.get("Content-Disposition") || "";
    const match = disposition.match(/filename="?([^";]+)"?/i);
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = match?.[1] || "temu-manual-orders.csv";
    document.body.appendChild(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    toast(`已导出当前筛选下的 ${state.manualMeta.total ?? state.manualOrders.length} 个人工订单`);
  } catch (error) {
    toast(error.message, true);
  } finally {
    setLoading(button, false);
  }
}

function renderManualOrders(focusOrder = "") {
  const items = state.manualOrders;
    $("#manual-rows").innerHTML = items.map((item) => {
        const lines = (item.lines || []).map((line) => `<span class="sku-line"><b>${escapeHtml(line.ext_code || "SKU待映射")}</b><span>× ${line.quantity}</span></span>`).join("");
	    const reasons = (item.reasons || []).map((reason) => `<span class="classification-tag">${manualReasonText(reason)}</span>`).join("");
	        const details = (item.details || []).map((detail) => `<span class="manual-detail"><i></i>${escapeHtml(detail)}</span>`).join("");
		    const mergeOrders = item.merge_order_sns?.length ? item.merge_order_sns.map(escapeHtml).join("、") : "-";
		        const reviewReasons = item.reasons || [];
			    const skuUnbound = reviewReasons.includes("sku_unbound");
			        const inventoryRule = reviewReasons.includes("inventory_rule");
				    const packageIncomplete = reviewReasons.includes("warehouse_sku_spec_incomplete");
				        let actions = `<button class="row-action" data-manual-action="approved" data-manual-order="${escapeHtml(item.parent_order_sn)}">批准自动发货</button>`;
					    if (skuUnbound) actions = `<button class="row-action" data-recheck-warehouse="${escapeHtml(item.parent_order_sn)}">重新校验绑定</button>`;
					        else if (packageIncomplete) actions = `<button class="row-action" data-recheck-warehouse="${escapeHtml(item.parent_order_sn)}">重新校验包裹数据</button>`;
						    else if (inventoryRule) actions = `<button class="row-action" data-recheck-warehouse="${escapeHtml(item.parent_order_sn)}">重新校验库存</button>`;
						        else if (item.status === "detected") actions = `<button class="row-action" data-manual-action="manual_pending" data-manual-order="${escapeHtml(item.parent_order_sn)}">转人工处理</button>`;
							    else if (item.status === "approved") actions = `<button class="row-action" data-manual-action="manual_pending" data-manual-order="${escapeHtml(item.parent_order_sn)}">重新转人工</button>`;
							        return `<tr ${focusOrder === item.parent_order_sn ? 'class="focused-row"' : ""}>
								      <td><div class="order-id"><strong>${escapeHtml(item.parent_order_sn)}</strong><small>识别 ${formatTime(item.detected_at)}</small></div></td>
								            <td><div class="classification-stack">${reasons || "-"}</div>${details ? `<div class="manual-detail-list">${details}</div>` : ""}</td>
									          <td><div class="sku-stack">${lines || "-"}</div></td>
										        <td class="merge-orders">${mergeOrders}</td>
											      <td><span class="status-badge ${item.status === "approved" ? "" : "pending"}">${manualStatusText(item.status)}</span></td>
											            <td><div class="manual-actions">${actions}</div></td>
												        </tr>`;
													  }).join("");
													    $("#manual-empty").hidden = items.length > 0;
    const searching = Boolean($("#manual-search").value.trim());
    $("#manual-empty strong").textContent = searching ? "没有匹配的人工订单" : "没有待人工订单";
    $("#manual-empty span").textContent = searching ? "请尝试其他订单号、SKU 或商品名称" : "同步后识别的一单多件与合并候选会显示在这里";
      $("#manual-total").textContent = `共 ${state.manualMeta.total ?? items.length} 条`;
        $("#nav-manual-count").textContent = state.manualMeta.total ?? items.length;
        renderPager("#manual-pager", state.manualMeta, "manual", loadManualOrders);
														  $$('[data-manual-action]').forEach((button) => button.addEventListener("click", () => updateManualOrder(button.dataset.manualOrder, button.dataset.manualAction)));
														    $$('[data-recheck-warehouse]').forEach((button) => button.addEventListener("click", () => recheckWarehouseEligibility(button.dataset.recheckWarehouse, button)));
														    }

async function recheckWarehouseEligibility(parentOrderSN, button) {
  setLoading(button, true);
    try {
        const { data } = await api(`/orders/${encodeURIComponent(parentOrderSN)}/warehouse-preview`, { method: "POST" });
	    if (data.inventory_error) throw new Error(data.inventory_error);
	        const categories = data.manual_categories || [];
		    if (categories.includes("inventory_rule")) toast("库存仍不符合自动发货安全线，订单继续保留在人工队列", true);
		        else if (categories.includes("sku_unbound")) toast("OMS 仍未返回该商品，请完成绑定后重试", true);
			    else if (categories.includes("warehouse_sku_spec_incomplete")) toast("仓库 SKU 的包裹数据仍不完整，请补齐后重试", true);
			        else toast("仓库库存、SKU 与包裹数据校验通过，人工分类已更新");
				  } catch (error) {
				      toast(error.message, true);
				        } finally {
					    setLoading(button, false);
					        await Promise.all([loadManualOrders(parentOrderSN), loadOrders()]);
						  }
						  }

async function updateManualOrder(parentOrderSN, status) {
  const purpose = status === "manual_pending" ? `将订单 ${parentOrderSN} 转入人工处理` : `批准订单 ${parentOrderSN} 进入自动发货`;
  const operationKey = await requireOperationKey(purpose); if (!operationKey) return;
  try {
    await api(`/orders/${encodeURIComponent(parentOrderSN)}/manual-review`, { method: "PUT", sensitive: true, body: JSON.stringify({ status }) });
    toast(status === "manual_pending" ? "订单已转入人工处理" : "订单已批准进入自动发货");
    await Promise.all([loadManualOrders(parentOrderSN), loadOrders(), loadShipments(), loadBulkFulfillment()]);
  } catch (error) { if (error.status === 401) forgetOperationKey(); toast(error.message, true); }
}

async function loadWarehouses(sync = false) {
  const path = sync ? "/warehouses/sync" : "/warehouses";
  const options = sync ? { method: "POST" } : {};
  try {
    const [warehouseResponse, policyResponse] = await Promise.all([api(path, options), api("/carrier-policies")]);
    const data = warehouseResponse.data || {};
    state.warehouses = data.warehouses || [];
    state.mappings = data.mappings || [];
    state.carrierPolicies = policyResponse.data || [];
    renderWarehouses();
    renderCarrierPolicies();
  } catch (error) { toast(error.message, true); }
}

const omsWarehouses = [
  { key: "DPS002", name: "DPS002", region: "美东", code: "DPSNY002" },
  { key: "ARP_EAST", name: "ARP美东", region: "美东", code: "HYTX30" },
  { key: "DPS004", name: "DPS004", region: "美西", code: "DPSCA004" },
  { key: "ARP_WEST", name: "ARP美西", region: "美西", code: "ARPCA01", disabled: true },
];

const omsAccounts = [
  { key: "dps", label: "DPS 账户" },
  { key: "arp", label: "ARP 账户" },
];

function renderWarehouses() {
  const enabled = state.warehouses.filter((warehouse) => warehouse.enable_buy_shipping_label);
  const mappingByKey = Object.fromEntries(state.mappings.map((mapping) => [mapping.oms_warehouse_key, mapping]));
  $("#warehouse-mappings").innerHTML = omsWarehouses.map((warehouse) => {
    if (warehouse.disabled) {
      return `<div class="mapping-row disabled"><div class="mapping-name"><strong>${warehouse.name}</strong><span>${warehouse.region} · 领星库存仓</span></div><div class="mapping-disabled"><strong>暂不启用</strong><span>仓库空置，PG1955 错误映射已移除</span></div></div>`;
    }
    const mapping = mappingByKey[warehouse.key] || {};
    return `<div class="mapping-row">
      <div class="mapping-name"><strong>${warehouse.name}</strong><span>${warehouse.region} · 业务仓标识</span></div>
      <label><span>领星账户</span><select data-oms-account="${warehouse.key}"><option value="">请选择账户</option>${omsAccounts.map((account) => `<option value="${account.key}" ${mapping.oms_account === account.key ? "selected" : ""}>${account.label}</option>`).join("")}</select></label>
      <label><span>领星仓库代码</span><input data-oms-code="${warehouse.key}" value="${escapeHtml(mapping.oms_warehouse_code || warehouse.code)}" /></label>
      <label><span>Temu Buy Label 仓库</span><select data-mapping-select="${warehouse.key}"><option value="">请选择仓库</option>${enabled.map((item) => `<option value="${escapeHtml(item.warehouse_id)}" ${mapping.temu_warehouse_id === item.warehouse_id ? "selected" : ""}>${escapeHtml(item.warehouse_name)} · ${escapeHtml(item.warehouse_id)}</option>`).join("")}</select></label>
      <div class="mapping-readonly"><small>领星同步</small><strong>只读自动查询</strong></div>
      <button class="secondary-button" data-save-mapping="${warehouse.key}">保存</button>
    </div>`;
  }).join("");
  $("#warehouse-rows").innerHTML = state.warehouses.map((warehouse) => `<tr><td><strong>${escapeHtml(warehouse.warehouse_name)}</strong></td><td>${escapeHtml(warehouse.warehouse_id)}</td><td>${warehouse.region_id || "-"}</td><td>${warehouse.warehouse_management_type}</td><td><span class="status-badge ${warehouse.enable_buy_shipping_label ? "" : "neutral"}">${warehouse.enable_buy_shipping_label ? "支持" : "不支持"}</span></td></tr>`).join("");
  $("#warehouse-total").textContent = `${state.warehouses.length} 个仓库`;
  $$('[data-save-mapping]').forEach((button) => button.addEventListener("click", () => saveMapping(button.dataset.saveMapping)));
}

function carrierPolicyGroup(warehouseKey) {
  return state.carrierPolicies.find((group) => group.warehouse_key === warehouseKey);
}

function renderCarrierPolicies() {
  const container = $("#carrier-policy-grid");
  $("#carrier-policy-shop").textContent = state.store.name || state.store.code;
  container.innerHTML = omsWarehouses.filter((warehouse) => !warehouse.disabled).map((warehouse) => {
    const group = carrierPolicyGroup(warehouse.key) || { warehouse_key: warehouse.key, carriers: [] };
    const carriers = [...(group.carriers || [])].sort((left, right) => left.priority - right.priority);
    const enabledCount = carriers.filter((carrier) => carrier.enabled).length;
    return `<section class="carrier-policy-card">
      <header><div><small>${escapeHtml(warehouse.region)}</small><strong>${escapeHtml(warehouse.name)}</strong></div><span>${enabledCount} / ${carriers.length} 启用</span><button class="secondary-button carrier-policy-save" data-save-carriers="${escapeHtml(warehouse.key)}"><svg><use href="#i-check"/></svg>保存</button></header>
      <div class="carrier-policy-list">${carriers.map((carrier, index) => `<div class="carrier-policy-item ${carrier.enabled ? "" : "disabled"}">
        <b>${index + 1}</b><div><strong>${escapeHtml(carrier.carrier_code)}</strong><small>${carrier.enabled ? `第 ${index + 1} 优先` : "已禁用"}</small></div>
        <div class="carrier-policy-order"><button class="icon-button" data-policy-move="up" data-policy-warehouse="${escapeHtml(warehouse.key)}" data-policy-carrier="${escapeHtml(carrier.carrier_code)}" ${index === 0 ? "disabled" : ""} title="上移 ${escapeHtml(carrier.carrier_code)}" aria-label="上移 ${escapeHtml(carrier.carrier_code)}"><svg><use href="#i-arrow-up"/></svg></button><button class="icon-button" data-policy-move="down" data-policy-warehouse="${escapeHtml(warehouse.key)}" data-policy-carrier="${escapeHtml(carrier.carrier_code)}" ${index === carriers.length - 1 ? "disabled" : ""} title="下移 ${escapeHtml(carrier.carrier_code)}" aria-label="下移 ${escapeHtml(carrier.carrier_code)}"><svg><use href="#i-arrow-down"/></svg></button></div>
        <label class="policy-switch" title="${carrier.enabled ? `禁用 ${escapeHtml(carrier.carrier_code)}` : `启用 ${escapeHtml(carrier.carrier_code)}`}"><input type="checkbox" data-policy-enabled="${escapeHtml(carrier.carrier_code)}" data-policy-warehouse="${escapeHtml(warehouse.key)}" ${carrier.enabled ? "checked" : ""} aria-label="${carrier.enabled ? `允许 ${escapeHtml(carrier.carrier_code)}` : `禁用 ${escapeHtml(carrier.carrier_code)}`}"><span></span></label>
      </div>`).join("")}</div>
    </section>`;
  }).join("");
  $$("[data-policy-move]").forEach((button) => button.addEventListener("click", () => moveCarrierPolicy(button.dataset.policyWarehouse, button.dataset.policyCarrier, button.dataset.policyMove)));
  $$("[data-policy-enabled]").forEach((input) => input.addEventListener("change", () => setCarrierEnabled(input.dataset.policyWarehouse, input.dataset.policyEnabled, input.checked)));
  $$("[data-save-carriers]").forEach((button) => button.addEventListener("click", () => saveCarrierPolicies(button.dataset.saveCarriers, button)));
}

function moveCarrierPolicy(warehouseKey, carrierCode, direction) {
  const group = carrierPolicyGroup(warehouseKey);
  if (!group) return;
  const carriers = group.carriers.sort((left, right) => left.priority - right.priority);
  const index = carriers.findIndex((carrier) => carrier.carrier_code === carrierCode);
  const target = direction === "up" ? index - 1 : index + 1;
  if (index < 0 || target < 0 || target >= carriers.length) return;
  [carriers[index], carriers[target]] = [carriers[target], carriers[index]];
  carriers.forEach((carrier, priority) => { carrier.priority = priority + 1; });
  renderCarrierPolicies();
}

function setCarrierEnabled(warehouseKey, carrierCode, enabled) {
  const carrier = carrierPolicyGroup(warehouseKey)?.carriers.find((item) => item.carrier_code === carrierCode);
  if (!carrier) return;
  carrier.enabled = enabled;
  renderCarrierPolicies();
}

async function saveCarrierPolicies(warehouseKey, button) {
  const group = carrierPolicyGroup(warehouseKey);
  if (!group) return toast("快递策略尚未加载", true);
  setLoading(button, true);
  try {
    const { data } = await api(`/carrier-policies/${encodeURIComponent(warehouseKey)}`, {
      method: "PUT",
      body: JSON.stringify({ carriers: group.carriers }),
    });
    const index = state.carrierPolicies.findIndex((item) => item.warehouse_key === warehouseKey);
    if (index >= 0) state.carrierPolicies[index] = data;
    else state.carrierPolicies.push(data);
    toast(`${warehouseKey} 快递策略已保存`);
    renderCarrierPolicies();
  } catch (error) {
    toast(error.message, true);
  } finally {
    setLoading(button, false);
  }
}

async function loadSKUWarehouseRules() {
  const query = $("#sku-rule-search").value.trim();
  const params = new URLSearchParams({ page: state.pages.skuRules, page_size: state.pageSize });
  if (query) params.set("q", query);
  try {
    const response = await api(`/sku-warehouse-rules?${params}`);
    state.skuWarehouseRules = response.data || [];
    state.skuRuleMeta = response.meta || {};
    if (adjustEmptyPage(state.skuRuleMeta, "skuRules", loadSKUWarehouseRules)) return;
    renderSKUWarehouseRules();
  } catch (error) {
    toast(error.message, true);
  }
}

function renderSKUWarehouseRules() {
  const configurableWarehouses = omsWarehouses.filter((warehouse) => !warehouse.disabled);
  const rows = $("#sku-rule-rows");
  rows.innerHTML = state.skuWarehouseRules.map((rule) => {
    const disabled = new Set(rule.disabled_warehouse_keys || []);
    const disabledCount = disabled.size;
    return `<tr class="${rule.dirty ? "dirty" : ""}" data-sku-rule-row="${escapeHtml(rule.warehouse_sku)}">
      <td><div class="sku-rule-identity"><code>${escapeHtml(rule.warehouse_sku)}</code><span>${escapeHtml(rule.product_name || "未记录商品名称")}</span></div></td>
      ${configurableWarehouses.map((warehouse) => {
        const allowed = !disabled.has(warehouse.key);
        return `<td><label class="warehouse-permission" title="${allowed ? "禁止" : "允许"} ${escapeHtml(rule.warehouse_sku)} 使用 ${escapeHtml(warehouse.name)}"><span>${allowed ? "允许" : "禁用"}</span><span class="policy-switch"><input type="checkbox" data-rule-sku="${escapeHtml(rule.warehouse_sku)}" data-rule-warehouse="${escapeHtml(warehouse.key)}" ${allowed ? "checked" : ""} aria-label="允许 ${escapeHtml(rule.warehouse_sku)} 使用 ${escapeHtml(warehouse.name)}"><span></span></span></label></td>`;
      }).join("")}
      <td><span class="status-badge ${disabledCount ? "pending" : "neutral"}" data-rule-status>${disabledCount ? `已禁用 ${disabledCount} 仓` : "默认全仓"}</span></td>
      <td><button class="secondary-button sku-rule-save" data-save-sku-rule="${escapeHtml(rule.warehouse_sku)}" ${rule.dirty ? "" : "disabled"}><svg><use href="#i-check"/></svg>保存</button></td>
    </tr>`;
  }).join("");
  const total = Number(state.skuRuleMeta.total || 0);
  const customized = state.skuWarehouseRules.filter((rule) => (rule.disabled_warehouse_keys || []).length > 0).length;
  $("#metric-sku-shop").textContent = state.store.name || state.store.code;
  $("#metric-sku-total").textContent = total;
  $("#metric-sku-customized").textContent = customized;
  $("#metric-sku-warehouses").textContent = configurableWarehouses.length;
  $("#sku-rule-total").textContent = `共 ${total} 个 SKU`;
  $("#sku-rule-empty").hidden = state.skuWarehouseRules.length > 0;
  renderPager("#sku-rule-pager", state.skuRuleMeta, "skuRules", loadSKUWarehouseRules);
  $$('[data-rule-sku]').forEach((input) => input.addEventListener("change", () => setSKUWarehouseAllowed(input.dataset.ruleSku, input.dataset.ruleWarehouse, input.checked)));
  $$('[data-save-sku-rule]').forEach((button) => button.addEventListener("click", () => saveSKUWarehouseRule(button.dataset.saveSkuRule, button)));
}

function setSKUWarehouseAllowed(warehouseSKU, warehouseKey, allowed) {
  const rule = state.skuWarehouseRules.find((item) => item.warehouse_sku === warehouseSKU);
  if (!rule) return;
  const disabled = new Set(rule.disabled_warehouse_keys || []);
  if (allowed) disabled.delete(warehouseKey);
  else disabled.add(warehouseKey);
  rule.disabled_warehouse_keys = omsWarehouses.map((warehouse) => warehouse.key).filter((key) => disabled.has(key));
  rule.customized = rule.disabled_warehouse_keys.length > 0;
  rule.dirty = true;
  renderSKUWarehouseRules();
}

async function saveSKUWarehouseRule(warehouseSKU, button) {
  const rule = state.skuWarehouseRules.find((item) => item.warehouse_sku === warehouseSKU);
  if (!rule) return;
  setLoading(button, true);
  try {
    const { data } = await api("/sku-warehouse-rules", {
      method: "PUT",
      body: JSON.stringify({ warehouse_sku: warehouseSKU, disabled_warehouse_keys: rule.disabled_warehouse_keys || [] }),
    });
    Object.assign(rule, data, { product_name: rule.product_name, dirty: false });
    toast(rule.customized ? `${warehouseSKU} 发货仓库限制已保存` : `${warehouseSKU} 已恢复默认全仓`);
    renderSKUWarehouseRules();
  } catch (error) {
    toast(error.message, true);
    setLoading(button, false);
  }
}

async function saveMapping(key) {
  const select = $(`[data-mapping-select="${key}"]`);
  const omsCode = $(`[data-oms-code="${key}"]`);
  const omsAccount = $(`[data-oms-account="${key}"]`);
  if (!select.value) return toast("请选择 Temu 仓库", true);
  if (!omsCode.value.trim()) return toast("请填写领星仓库代码", true);
  if (!omsAccount.value) return toast("请选择领星账户", true);
  const operationKey = await requireOperationKey("保存仓库映射"); if (!operationKey) return;
  try {
    await api(`/warehouse-mappings/${key}`, {
      method: "PUT", sensitive: true,
      body: JSON.stringify({
        temu_warehouse_id: select.value,
        oms_warehouse_code: omsCode.value.trim(),
        oms_account: omsAccount.value,
      }),
    });
    toast(`${key} 仓库配置已保存`); await loadWarehouses();
  } catch (error) { if (error.status === 401) forgetOperationKey(); toast(error.message, true); }
}

async function openFulfillment(parentOrderSN, recoveryShipment = null) {
  state.warehouseController?.abort();
  state.quoteController?.abort();
  let order = state.orders.find((item) => item.parent_order_sn === parentOrderSN);
  if (!order) {
    try {
      const payload = await api(`/orders/${encodeURIComponent(parentOrderSN)}`);
      order = payload.data;
    } catch (error) {
      toast(error.message, true);
      return;
    }
  }
  state.currentOrder = order;
  state.recoveryShipment = recoveryShipment;
  state.warehousePreview = null; state.quote = null; state.selectedChannelId = 0;
  clearTimeout(state.quoteTimer); state.quoteSequence += 1;
  setQuoteStatus("pending", "等待库存校验");
  if (!state.currentOrder) return;
  $("#fulfillment-title").textContent = state.currentOrder.parent_order_sn;
  const recoverySummary = recoveryShipment
    ? `<div class="summary-block recovery-summary"><small>上次购单失败</small><strong>${escapeHtml(recoveryShipment.shipping_company_name || "-")} · ${escapeHtml(recoveryShipment.ship_logistics_type || "-")}</strong><span>${escapeHtml(recoveryShipment.package_sn_list?.join("、") || "未返回包裹号")} · 本次自动排除该承运商</span></div>`
    : `<div class="summary-block"><small>最晚发货</small><strong>${relativeDeadline(state.currentOrder.expect_ship_latest_time)} · ${formatTime(state.currentOrder.expect_ship_latest_time)}</strong></div>`;
  $("#order-summary").innerHTML = `<div class="summary-block"><small>商品</small><strong>${(state.currentOrder.lines || []).map((line) => `${escapeHtml(line.ext_code)} × ${line.quantity}`).join(" · ")}</strong></div>${recoverySummary}`;
  $("#purchase-button").textContent = recoveryShipment ? "确认重新购买面单" : "确认并购买面单";
  $("#quote-result").hidden = true;
  $("#quote-form").hidden = true;
  $("#inventory-matrix").hidden = true;
  setWarehouseCheck("pending", "正在连接领星 Go 服务", "正在实时查询三个启用仓");
  $("#fulfillment-dialog").showModal();
  await loadWarehousePreview(parentOrderSN);
}

function setWarehouseCheck(tone, title, detail) {
  $("#warehouse-check-title").textContent = title;
  const message = $("#warehouse-check-message");
  message.className = `warehouse-check-message ${tone}`;
  const dotClass = tone === "ready" ? "" : tone === "pending" || tone === "warning" ? "warning" : "error";
  message.innerHTML = `<span class="dot ${dotClass}"></span><div><strong>${escapeHtml(title)}</strong><small>${escapeHtml(detail || "")}</small></div>`;
}

async function loadWarehousePreview(parentOrderSN = state.currentOrder?.parent_order_sn) {
  if (!parentOrderSN) return;
  state.warehouseController?.abort();
  state.quoteController?.abort();
  state.quoteSequence += 1;
  const controller = new AbortController();
  state.warehouseController = controller;
  const button = $("#refresh-warehouse-preview");
  setLoading(button, true);
  state.warehousePreview = null;
  state.quote = null;
  $("#quote-result").hidden = true;
  $("#quote-form").hidden = true;
  setWarehouseCheck("pending", "正在连接领星 Go 服务", "实时读取正品产品可用库存（最长 35 秒）");
  try {
    const previewPath = state.recoveryShipment
      ? `/shipments/${encodeURIComponent(state.recoveryShipment.id)}/recovery/warehouse-preview`
      : `/orders/${encodeURIComponent(parentOrderSN)}/warehouse-preview`;
    const { data } = await api(previewPath, { method: "POST", signal: controller.signal, timeoutMs: 35000 });
    if (state.currentOrder?.parent_order_sn !== parentOrderSN) return;
    state.warehousePreview = data;
    if (data.requires_manual) void Promise.all([loadManualOrders(), loadOrders()]);
    if ((data.manual_categories || []).includes("inventory_rule")) toast("库存不符合自动发货安全线，已自动转入人工订单");
    renderWarehousePreview();
    if (data.ready) scheduleQuote(0);
  } catch (error) {
    if (error.code === "REQUEST_ABORTED" || state.currentOrder?.parent_order_sn !== parentOrderSN) return;
    $("#inventory-matrix").hidden = true;
    const timedOut = error.code === "REQUEST_TIMEOUT" || error.status === 504;
    setWarehouseCheck("failed", timedOut ? "库存查询超时" : "无法获取发货仓库", timedOut ? "领星或上游响应超时，请点击刷新重试" : error.message);
  } finally {
    if (state.warehouseController === controller) {
      state.warehouseController = null;
      setLoading(button, false);
    }
  }
}

function inventoryWarehouse(record, key) {
  for (const region of record.regions || []) {
    const warehouse = (region.warehouses || []).find((item) => item.warehouse_key === key);
    if (warehouse) return warehouse;
  }
  return null;
}

function stockCell(warehouse) {
  if (!warehouse) return '<span class="stock-cell blocked"><strong>-</strong><small>未返回</small></span>';
  const amount = Number(warehouse.available_amount || 0);
  const stateText = warehouse.shop_sku_disabled ? "店铺禁用" : !warehouse.active ? "未启用" : warehouse.query_status !== "succeeded" ? "查询失败" : amount <= 0 ? "无库存" : warehouse.selectable ? "可用" : "不可选";
  const tone = warehouse.selectable && amount > 0 ? "ready" : "blocked";
  return `<span class="stock-cell ${tone}" title="${escapeHtml(warehouse.reason || "")}"><strong>${escapeHtml(Number.isInteger(amount) ? amount : amount.toFixed(2))}</strong><small>${stateText}</small></span>`;
}

function previewWarehouse(key) {
  const records = state.warehousePreview?.decision?.records || [];
  for (const record of records) {
    for (const region of record.regions || []) {
      const warehouse = (region.warehouses || []).find((item) => item.warehouse_key === key);
      if (warehouse) return warehouse;
    }
  }
  return null;
}


function recordIsOMSUnbound(record) {
  const activeWarehouses = (record.regions || []).flatMap((region) => region.warehouses || []).filter((warehouse) => warehouse.active);
  return activeWarehouses.length > 0
    && activeWarehouses.every((warehouse) => warehouse.query_status === "succeeded")
    && activeWarehouses.every((warehouse) => !warehouse.sku_found);
}

function regionDecisionItems(option) {
  const preview = state.warehousePreview;
  const blockedStock = (preview?.decision?.records || []).flatMap((record) => {
    if (recordIsOMSUnbound(record)) return [{ tone: "blocked", text: `${record.sku}：OMS 未绑定，需人工完成 SKU 绑定` }];
    const current = (record.regions || []).find((region) => region.region === option.region);
    return current?.requires_manual ? [{ tone: "blocked", text: `${record.sku}：${current.reason}` }] : [];
  });
  if (!option.warehouse_key) {
    if (blockedStock.length) return blockedStock;
    return [{ tone: "blocked", text: option.error || "该区域没有可覆盖整单的仓库" }];
  }

  const dpsKey = option.region === "east" ? "DPS002" : "DPS004";
  const arpKey = option.region === "east" ? "ARP_EAST" : "ARP_WEST";
  const dpsName = previewWarehouse(dpsKey)?.warehouse_name || dpsKey;
  const arpName = previewWarehouse(arpKey)?.warehouse_name || arpKey;
  if (!option.reason && option.error) return [{ tone: "blocked", text: option.error }];
  const items = [];
  if (option.warehouse_key === dpsKey) {
    items.push({ tone: "ready", text: `${dpsName} 可独立覆盖整单全部 SKU` });
    if (option.recommended) items.push({ tone: "selected", text: `规则默认优先 ${dpsName}，先清理 DPS 库存` });
  } else if (option.warehouse_key === arpKey) {
    items.push({ tone: "ready", text: `${arpName} 可独立覆盖整单全部 SKU` });
    if (option.recommended) {
      items.push({ tone: "warning", text: `${dpsName} 无法独立覆盖整单全部 SKU` });
      items.push({ tone: "selected", text: `默认回退 ${arpName} 发货` });
    } else {
      items.push({ tone: "warning", text: `${dpsName} 同样可发，规则默认优先 DPS` });
      items.push({ tone: "selected", text: `${arpName} 可人工改选` });
    }
  }
  if (option.error) {
    items.push({ tone: "blocked", text: option.error });
  } else if (option.mapping?.ready) {
    items.push({ tone: "ready", text: `Temu 发货仓：${option.mapping.warehouse_name}` });
  }
  return items;
}

function renderDecisionPoints(items) {
  return `<span class="decision-points">${items.map((item) => `<span class="decision-point ${item.tone}"><i></i><span>${escapeHtml(item.text)}</span></span>`).join("")}</span>`;
}

function renderPackageResolution(resolution) {
  const panel = $("#package-spec-panel");
  const items = resolution.items || [];
  panel.hidden = items.length === 0;
  if (items.length === 0) return;
  const labels = { warehouse_sku: "未建立规格记录", enabled: "规格已停用", length_cm: "缺少长度", width_cm: "缺少宽度", height_cm: "缺少高度", weight_kg: "缺少重量" };
  const badge = $("#package-spec-badge");
  badge.className = `status-badge ${resolution.complete ? "" : "failed"}`;
  badge.textContent = resolution.complete ? "已匹配" : "阻断发货";
  $("#package-spec-title").textContent = resolution.complete ? "已按仓库 SKU 生成包裹" : "仓库 SKU 规格不完整";
  $("#package-spec-items").innerHTML = items.map((item) => {
    const missing = (item.missing_fields || []).map((field) => labels[field] || field);
    const spec = item.complete
      ? `${item.weight_kg} kg · ${item.length_cm} × ${item.width_cm} × ${item.height_cm} cm`
      : missing.join(" · ") || "规格不可用";
    const matchedSKU = item.matched_warehouse_sku || item.warehouse_sku;
    const matchHint = item.match_type === "one_m_compat" ? ` → ${matchedSKU}（兼容匹配）` : "";
    const editable = item.matched && item.enabled;
    return `<div class="package-spec-item ${item.complete ? "ready" : "blocked"}"><span class="dot ${item.complete ? "" : "error"}"></span><div class="package-spec-copy"><strong>${escapeHtml(item.warehouse_sku + matchHint)} × ${item.quantity}</strong><small>${escapeHtml(spec)}</small></div>${editable ? `<button class="icon-button package-spec-edit" type="button" data-edit-package-spec="${escapeHtml(matchedSKU)}" title="修改仓库 SKU 包裹规格" aria-label="修改仓库 SKU 包裹规格"><svg><use href="#i-pencil"/></svg></button>` : ""}</div>
      ${editable ? `<form class="package-spec-editor" data-package-spec-form="${escapeHtml(matchedSKU)}" hidden><label><span>重量 kg</span><input name="weight_kg" type="number" min="0.001" step="0.001" value="${escapeHtml(item.weight_kg ?? "")}" required></label><label><span>长度 cm</span><input name="length_cm" type="number" min="0.01" step="0.01" value="${escapeHtml(item.length_cm ?? "")}" required></label><label><span>宽度 cm</span><input name="width_cm" type="number" min="0.01" step="0.01" value="${escapeHtml(item.width_cm ?? "")}" required></label><label><span>高度 cm</span><input name="height_cm" type="number" min="0.01" step="0.01" value="${escapeHtml(item.height_cm ?? "")}" required></label><div class="package-spec-editor-actions"><button class="secondary-button" type="button" data-cancel-package-spec>取消</button><button class="primary-button" type="submit"><svg><use href="#i-check"/></svg><span>保存并重新询价</span></button></div></form>` : ""}`;
  }).join("");
  $$('[data-edit-package-spec]').forEach((button) => {
    const warehouseSKU = button.dataset.editPackageSpec;
    const form = $$('[data-package-spec-form]').find((item) => item.dataset.packageSpecForm === warehouseSKU);
    if (!form) return;
    button.addEventListener("click", () => {
      form.hidden = false;
      button.hidden = true;
      clearTimeout(state.quoteTimer);
      state.quoteController?.abort();
      state.quoteSequence += 1;
      state.quote = null;
      $("#quote-result").hidden = true;
      setQuoteStatus("pending", "修改仓库 SKU 规格后保存并重新查询");
      form.querySelector("input")?.focus();
    });
    form.querySelector('[data-cancel-package-spec]').addEventListener("click", () => {
      form.hidden = true;
      button.hidden = false;
      scheduleQuote(0);
    });
    form.addEventListener("submit", (event) => savePackageSpec(event, warehouseSKU));
  });
}

async function savePackageSpec(event, warehouseSKU) {
  event.preventDefault();
  const form = event.currentTarget;
  if (!form.reportValidity()) return;
  const submit = form.querySelector('button[type="submit"]');
  const values = new FormData(form);
  const payload = { weight_kg: Number(values.get("weight_kg")), length_cm: Number(values.get("length_cm")), width_cm: Number(values.get("width_cm")), height_cm: Number(values.get("height_cm")) };
  clearTimeout(state.quoteTimer);
  state.quoteController?.abort();
  state.quoteSequence += 1;
  state.quote = null;
  $("#quote-result").hidden = true;
  setLoading(submit, true);
  setQuoteStatus("pending", "正在保存仓库 SKU 包裹规格");
  try {
    await api(`/warehouse-sku-specs/${encodeURIComponent(warehouseSKU)}/package`, { method: "PATCH", body: JSON.stringify(payload) });
    toast(`${warehouseSKU} 包裹规格已保存`);
    await loadWarehousePreview();
  } catch (error) {
    setQuoteStatus("failed", "仓库 SKU 规格保存失败");
    toast(error.message, true);
  } finally {
    setLoading(submit, false);
  }
}

function renderWarehousePreview() {
  const preview = state.warehousePreview;
  if (!preview) return;
  const decision = preview.decision || {};
  renderPackageResolution(decision.package_resolution || {});
  const records = decision.records || [];
  const defaults = decision.default_thresholds || { east_threshold: Number(decision.safety_stock_threshold ?? 0), west_threshold: Number(decision.safety_stock_threshold ?? 0), total_threshold: 0 };
  $("#inventory-rule").textContent = `规则 ${escapeHtml(decision.rule_version || "-")} · 默认安全线 东 ${defaults.east_threshold} / 西 ${defaults.west_threshold} / 总 ${defaults.total_threshold}`;
  $("#inventory-time").textContent = decision.queried_at ? `库存时间 ${formatTime(decision.queried_at)}` : "库存时间 -";
  $("#inventory-preview-rows").innerHTML = records.map((record) => {
    const manualReasons = (record.regions || []).filter((region) => region.requires_manual).map((region) => `${region.region_name}：${region.reason}`);
    const conclusion = record.requires_manual
      ? `<span class="inventory-conclusion blocked"><strong>转人工</strong><small>${escapeHtml(manualReasons.join("；") || record.reason)}</small></span>`
      : '<span class="inventory-conclusion ready"><strong>自动选仓可用</strong><small>东西区域库存均高于安全线</small></span>';
    const thresholds = record.thresholds || defaults;
    return `<tr>
      <td><div class="order-id"><strong>${escapeHtml(record.sku)}</strong><small>需发 ${preview.quantities?.[record.sku] ?? 0} · 安全线 东${thresholds.east_threshold} / 西${thresholds.west_threshold} / 总${thresholds.total_threshold}</small></div></td>
      <td>${stockCell(inventoryWarehouse(record, "DPS002"))}</td>
      <td>${stockCell(inventoryWarehouse(record, "ARP_EAST"))}</td>
      <td>${stockCell(inventoryWarehouse(record, "DPS004"))}</td>
      <td>${stockCell(inventoryWarehouse(record, "ARP_WEST"))}</td>
      <td>${conclusion}</td>
    </tr>`;
  }).join("");

  const readyOptions = (preview.regions || []).filter((option) => option.ready);
  const selectedOption = readyOptions.find((option) => option.recommended) || readyOptions[0];
  $("#warehouse-region-options").innerHTML = (preview.regions || []).map((option) => {
    const mappingText = option.warehouse_key === "ARP_WEST" ? "暂不启用" : option.mapping?.ready ? "Temu：" + option.mapping.warehouse_name : "Temu 仓未映射";
    const checked = option === selectedOption;
    return `<label class="warehouse-region-choice ${option.ready ? "ready" : "blocked"}">
      <input type="radio" name="warehouse_key" value="${escapeHtml(option.warehouse_key || "")}" data-region="${escapeHtml(option.region)}" form="quote-form" ${checked ? "checked" : ""} ${option.ready ? "required" : "disabled"} />
      <span class="region-radio"></span>
      <div><small>${escapeHtml(option.region_name)}${option.recommended ? " · 默认推荐" : ""}</small><strong>${escapeHtml(option.warehouse_name || option.warehouse_key || "不可自动发货")}</strong>${renderDecisionPoints(regionDecisionItems(option))}</div>
      <div class="mapping-state"><small>${escapeHtml(option.warehouse_key || "-")}</small><strong>${escapeHtml(mappingText)}</strong></div>
    </label>`;
  }).join("");
  $$('input[name="warehouse_key"][form="quote-form"]').forEach((input) => input.addEventListener("change", () => {
    state.quote = null;
    $("#quote-result").hidden = true;
    $$(".warehouse-region-choice").forEach((item) => item.classList.toggle("selected", item.contains(input) && input.checked));
    scheduleQuote(0);
  }));
  const selected = $('input[name="warehouse_key"][form="quote-form"]:checked');
  selected?.closest(".warehouse-region-choice")?.classList.add("selected");

  $("#open-warehouse-mappings").hidden = !preview.mapping_required;
  $("#inventory-matrix").hidden = records.length === 0 && (preview.regions || []).length === 0;
  $("#quote-form").hidden = !preview.ready;
  if ((preview.manual_categories || []).includes("warehouse_sku_spec_incomplete")) {
    setQuoteStatus("failed", "仓库 SKU 包裹规格不完整");
  }
  if (preview.inventory_error) {
    setWarehouseCheck("failed", "领星库存查询不完整", preview.inventory_error);
  } else if ((preview.manual_categories || []).includes("warehouse_sku_spec_incomplete")) {
    setWarehouseCheck("failed", "仓库 SKU 包裹规格缺失", (preview.manual_reasons || []).join("；"));
  } else if ((preview.manual_categories || []).includes("sku_unbound")) {
    setWarehouseCheck("failed", "发现 OMS 未绑定商品", "已转人工绑定，完成绑定前不能推单到 OMS");
  } else if (preview.requires_manual) {
    setWarehouseCheck("failed", "库存规则要求转人工", (preview.manual_reasons || []).join("；"));
  } else if (!preview.ready && preview.mapping_required) {
    setWarehouseCheck("warning", "库存校验通过，仓库映射缺失", "先将推荐的领星仓映射到 Temu Buy Label 仓");
  } else if (!preview.ready) {
    setWarehouseCheck("failed", "没有可自动发货的仓库", "查看下方区域与库存结论");
  } else {
    setWarehouseCheck("ready", "领星实时库存校验通过", "仓库 SKU 规格已匹配，正在自动查询 Temu 物流渠道");
  }
}

function quoteRequest(preferredChannelId = 0) {
  const form = new FormData($("#quote-form"));
  const selected = $('input[name="warehouse_key"][form="quote-form"]:checked');
  return { parent_order_sn: state.currentOrder.parent_order_sn, region: selected?.dataset.region || "", warehouse_key: form.get("warehouse_key"), preferred_channel_id: preferredChannelId };
}

function setQuoteStatus(tone, text) {
  const element = $("#quote-status");
  element.className = "auto-query-status " + tone;
  const dot = element.querySelector(".dot");
  dot.className = "dot" + (tone === "ready" ? "" : tone === "failed" ? " error" : " warning");
  element.querySelector("strong").textContent = text;
  $("#retry-quote").hidden = true;
}

function scheduleQuote(delay = 450) {
  clearTimeout(state.quoteTimer);
  const form = $("#quote-form");
  if (!state.warehousePreview?.ready || !$("#fulfillment-dialog").open || !form.checkValidity()) return;
  state.quoteTimer = setTimeout(() => { createQuote().catch(() => {}); }, delay);
}

async function createQuote(preferredChannelId = 0) {
  clearTimeout(state.quoteTimer);
  state.quoteController?.abort();
  const controller = new AbortController();
  state.quoteController = controller;
  const parentOrderSN = state.currentOrder?.parent_order_sn;
  const sequence = ++state.quoteSequence;
  setQuoteStatus("pending", "正在自动查询实时库存与物流渠道（最长 35 秒）");
  try {
    const request = quoteRequest(preferredChannelId);
    const quotePath = state.recoveryShipment
      ? `/shipments/${encodeURIComponent(state.recoveryShipment.id)}/recovery/quotes`
      : "/shipping/quotes";
    const { data } = await api(quotePath, { method: "POST", body: JSON.stringify(request), signal: controller.signal, timeoutMs: 35000 });
    if (sequence !== state.quoteSequence || state.currentOrder?.parent_order_sn !== parentOrderSN) return null;
    state.quote = data;
    state.selectedChannelId = data.quote.selected_channel_id;
    renderQuote();
    return data;
  } catch (error) {
    if (error.code === "REQUEST_ABORTED") return null;
    if (sequence === state.quoteSequence) {
      const timedOut = error.code === "REQUEST_TIMEOUT" || error.status === 504;
      setQuoteStatus("failed", timedOut ? "物流渠道查询超时" : "自动查询失败");
      $("#retry-quote").hidden = false;
      toast(timedOut ? "Temu 物流查询超时，请重试" : error.message, true);
    }
    throw error;
  } finally {
    if (state.quoteController === controller) state.quoteController = null;
  }
}
function renderQuote() {
  const data = state.quote;
  const availableChannels = data.available_channels || [];
  const unavailableChannels = data.unavailable_channels || [];
  setQuoteStatus("ready", "物流渠道已自动查询完成");
  $("#quote-result").hidden = false;
  const previewRegion = (state.warehousePreview?.regions || []).find((item) => item.warehouse_key === data.warehouse_selection.warehouse_key);
  const decisionItems = previewRegion ? regionDecisionItems(previewRegion) : [{ tone: "selected", text: data.warehouse_selection.reason }];
  $("#warehouse-decision").innerHTML = `<strong>${escapeHtml(data.warehouse_selection.warehouse_name)}</strong> → ${escapeHtml(data.temu_warehouse.warehouse_name)}${renderDecisionPoints(decisionItems)}`;
  $("#quote-expiry").textContent = `报价有效至 ${formatTime(data.quote.expires_at)}`;
  $("#channel-count").textContent = "Temu 共返回 " + (availableChannels.length + unavailableChannels.length) + " 个渠道：" + availableChannels.length + " 个可用，" + unavailableChannels.length + " 个不可用";
  $("#channel-list").innerHTML = availableChannels.map((channel) => `<button type="button" class="channel-option ${channel.channelId === state.selectedChannelId ? "selected" : ""}" data-channel="${channel.channelId}"><span class="channel-radio"></span><div><strong>${escapeHtml(channel.shippingCompanyName)}</strong><small>${escapeHtml(channel.shipLogisticsType)}</small></div><div><strong>${escapeHtml(channel.estimatedText || "Temu 可用")}</strong><small>${escapeHtml(channel.channelRules || channel.selection_reason || "当前包裹可用")}</small></div><span class="channel-price">${escapeHtml(channel.estimatedAmount || "-")}</span></button>`).join("");
  const unavailablePanel = $("#unavailable-channels");
  unavailablePanel.hidden = unavailableChannels.length === 0;
  $("#unavailable-channel-count").textContent = unavailableChannels.length + " 个不可用";
  $("#unavailable-channel-list").innerHTML = unavailableChannels.map((channel) => `<div class="unavailable-channel"><i></i><strong>${escapeHtml(channel.shippingCompanyName || "未知渠道")} · ${escapeHtml(channel.shipLogisticsType || "-")}</strong><span>${escapeHtml(channel.unavailableReason || "Temu 未提供不可用原因")}</span></div>`).join("");
  $$('[data-channel]').forEach((button) => button.addEventListener("click", () => { state.selectedChannelId = Number(button.dataset.channel); $$('.channel-option').forEach((item) => item.classList.toggle("selected", item === button)); }));
}

async function purchaseLabel() {
  if (!state.quote) return;
  const button = $("#purchase-button"); setLoading(button, true); button.textContent = "正在占用订单并提交购单";
  try {
    if (state.selectedChannelId !== state.quote.quote.selected_channel_id) await createQuote(state.selectedChannelId);
    const purchasePath = state.recoveryShipment
      ? `/shipments/${encodeURIComponent(state.recoveryShipment.id)}/recovery/resubmit`
      : "/shipping/purchase";
    const { data } = await api(purchasePath, { method: "POST", body: JSON.stringify({ quote_id: state.quote.quote.id, confirm: true }) });
    toast(state.recoveryShipment ? "新物流面单已重新提交，旧包裹号已保留在审计记录" : data.duplicate ? "该订单已有发货记录，未重复提交" : "面单购买请求已提交，后台将自动确认并等待领星");
    $("#fulfillment-dialog").close(); await Promise.all([loadOrders(), loadShipments(), loadBulkFulfillment()]); switchView("labels");
  } catch (error) { toast(error.message, true); }
  finally { setLoading(button, false); button.textContent = state.recoveryShipment ? "确认重新购买面单" : "确认并购买面单"; }
}
async function exportShipmentPO() {
  const button = $("#export-ledger-po");
  setLoading(button, true);
  try {
    const fromValue = $("#export-po-from").value;
    const toValue = $("#export-po-to").value;
    const fromDate = fromValue ? new Date(fromValue) : null;
    const toDate = toValue ? new Date(toValue) : null;
    if (fromDate && toDate && fromDate > toDate) throw new Error("导出开始时间不能晚于结束时间");
    const params = new URLSearchParams();
    if (fromDate) params.set("from", fromDate.toISOString());
    if (toDate) params.set("to", toDate.toISOString());
    const query = params.toString();
    const response = await fetch(apiPath(`/shipments/export-po.zip${query ? `?${query}` : ""}`), { cache: "no-store", headers: { "X-Temu-Shop": state.store.code } });
    if (!response.ok) {
      let message = `导出失败（${response.status}）`;
      try {
        const payload = await response.json();
        message = payload.error || message;
      } catch (_) {}
      throw new Error(message);
    }
    const blob = await response.blob();
    const disposition = response.headers.get("Content-Disposition") || "";
    const match = disposition.match(/filename="?([^";]+)"?/i);
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = match?.[1] || "temu-auto-shipment-po-by-warehouse.zip";
    document.body.appendChild(link);
    link.click();
    link.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    toast("全部自动发货 PO 已按仓库分别导出");
  } catch (error) {
    toast(error.message, true);
  } finally {
    setLoading(button, false);
  }
}

async function loadShipments() {
  await Promise.all([loadShipmentQueue("labels"), loadShipmentQueue("exceptions"), loadShipmentQueue("ledger"), loadHistory()]);
}

async function loadShipmentQueue(key) {
  const queue = key === "labels" ? "processing" : key === "exceptions" ? "exceptions" : "ledger";
  try {
    const payload = await api(`/shipments?page=${state.pages[key]}&page_size=${state.pageSize}&queue=${queue}`);
    if (adjustEmptyPage(payload.meta, key, () => loadShipmentQueue(key))) return;
    if (key === "labels") {
      state.labelShipments = payload.data || [];
      state.labelMeta = payload.meta || {};
      renderLabelQueue();
    } else if (key === "exceptions") {
      state.exceptionShipments = payload.data || [];
      state.exceptionMeta = payload.meta || {};
      renderExceptionQueue();
    } else {
      state.shipments = payload.data || [];
      state.shipmentMeta = payload.meta || {};
      renderShipments();
    }
  } catch (error) { toast(error.message, true); }
}

async function loadHistory() {
  try {
    const payload = await api(`/orders/history?page=${state.pages.history}&page_size=${state.pageSize}`);
    if (adjustEmptyPage(payload.meta, "history", loadHistory)) return;
    state.history = payload.data || [];
    state.historyMeta = payload.meta || {};
    renderHistory();
  } catch (error) { toast(error.message, true); }
}

function renderHistory() {
  $("#history-rows").innerHTML = state.history.map((order) => {
    const detail = order.detail || {};
    const lines = (order.lines || []).map((line) => `<span class="sku-line"><b>${escapeHtml(line.ext_code || "SKU待映射")}</b><span>× ${line.quantity}</span></span>`).join("");
    const mergeFlags = [...(detail.batch_order_sns || []), ...(detail.platform_consolidated ? ["Temu 已合并"] : [])];
    return `<tr>
      <td><div class="order-id"><strong>${escapeHtml(order.parent_order_sn)}</strong><small>更新 ${formatTime(order.update_time)}</small></div></td>
      <td><div class="sku-stack">${lines || "-"}</div></td>
      <td>${escapeHtml((detail.region_names || []).join(" / ") || "-")}</td>
      <td class="merge-orders">${mergeFlags.length ? mergeFlags.map(escapeHtml).join("、") : "-"}</td>
      <td><span class="status-badge ${order.open ? "pending" : "neutral"}">${order.open ? "待发货" : "已离开待发货"}</span></td>
      <td>${formatTime(detail.fetched_at)}</td>
    </tr>`;
  }).join("");
  $("#history-empty").hidden = state.history.length > 0;
  $("#history-total").textContent = `${state.historyMeta.total ?? state.history.length} 条详情`;
  renderPager("#history-pager", state.historyMeta, "history", loadHistory);
}

function omsSyncCell(shipment) {
  const sync = shipment.oms_sync;
  const warehouse = [shipment.oms_warehouse_key, shipment.oms_warehouse_code].filter(Boolean).join(" · ") || "仓库配置缺失";
  if (shipment.status !== "shipped") {
    return `<div class="oms-sync-check"><span class="status-badge neutral">后台处理中</span><small>${escapeHtml(warehouse)}</small></div>`;
  }
  const statusLabels = {
    querying: "正在自动查询领星",
    waiting_sync: "等待领星确认",
    verified: "领星已确认",
    failed: "等待重试领星查询",
    manual_required: "领星需人工处理",
  };
  const tone = sync?.status === "verified" ? "" : ["failed", "manual_required"].includes(sync?.status) ? "failed" : "pending";
  const label = sync ? statusLabels[sync.status] || sync.status : "等待领星确认";
  const detail = sync?.error_message || warehouse;
  return `<div class="oms-sync-check"><span class="status-badge ${tone}">${escapeHtml(label)}</span><small title="${escapeHtml(detail)}">${escapeHtml(detail)}</small></div>`;
}
function shipmentRow(shipment, withRecoveryAction = false) {
	const retryInfo = shipment.status === "submission_unknown"
		? `<small>安全重提 ${shipment.submission_attempts || 1}/3 · 每次间隔 2 分钟</small>`
		: shipment.status === "confirm_failed"
			? `<small>确认重试 ${shipment.confirmation_attempts || 1}/3</small>`
		: "";
  return `<tr>
    <td><div class="order-id"><strong>${escapeHtml(shipment.parent_order_sn)}</strong><small>${escapeHtml(shipment.id)}</small></div></td>
    <td><div class="order-id"><strong>${escapeHtml(shipment.shipping_company_name || "-")}</strong><small>${escapeHtml(shipment.ship_logistics_type || "-")}</small></div></td>
    <td><div class="order-id"><strong>${escapeHtml(shipment.package_sn_list?.join("、") || "等待包裹号")}</strong><small>${escapeHtml(shipment.tracking_number || "等待跟踪号")}</small></div></td>
	<td><span class="status-badge ${statusClass(shipment.status)}">${statusText(shipment.status)}</span>${shipment.error_message ? `<div class="order-id"><small title="${escapeHtml(shipment.error_message)}">${escapeHtml(shipment.error_message)}</small>${retryInfo}</div>` : retryInfo}</td>
    <td>${omsSyncCell(shipment)}</td>
    <td>${formatTime(shipment.created_at)}</td>
    ${withRecoveryAction ? `<td>${shipment.status === "label_failed" && !shipment.tracking_number && !shipment.confirmed_at && !(shipment.package_sn_list || []).length ? `<button class="row-action recovery-action" data-recover-shipment="${escapeHtml(shipment.id)}">重新选择并发货</button>` : shipment.status === "label_failed" && (shipment.package_sn_list || []).length ? `<span class="status-badge pending">Temu 后台切换自发货</span>` : "-"}</td>` : ""}
  </tr>`;
}

function renderLabelQueue() {
  const items = state.labelShipments;
  const counts = state.labelMeta.status_counts || {};
  const rows = $("#label-rows");
  rows.innerHTML = items.map((shipment) => shipmentRow(shipment)).join("");
  $("#labels-empty").hidden = items.length > 0;
  $("#metric-label-submitting").textContent = counts.submitting || 0;
  $("#metric-label-pending").textContent = counts.label_pending || 0;
  $("#metric-label-ready").textContent = counts.label_ready || 0;
  $("#metric-label-total").textContent = state.labelMeta.total ?? items.length;
  $("#nav-label-count").textContent = state.labelMeta.total ?? items.length;
  renderPager("#labels-pager", state.labelMeta, "labels", () => loadShipmentQueue("labels"));
}

function renderExceptionQueue() {
  const items = state.exceptionShipments;
  const counts = state.exceptionMeta.status_counts || {};
  const rows = $("#exception-rows");
  rows.innerHTML = items.map((shipment) => shipmentRow(shipment, true)).join("");
  $("#exceptions-empty").hidden = items.length > 0;
  $("#metric-exception-unknown").textContent = counts.submission_unknown || 0;
  $("#metric-exception-label").textContent = counts.label_failed || 0;
  $("#metric-exception-confirm").textContent = counts.confirm_failed || 0;
  $("#metric-exception-total").textContent = state.exceptionMeta.total ?? items.length;
  $("#nav-exception-count").textContent = state.exceptionMeta.total ?? items.length;
  renderPager("#exceptions-pager", state.exceptionMeta, "exceptions", () => loadShipmentQueue("exceptions"));
  $$('[data-recover-shipment]').forEach((button) => button.addEventListener("click", () => openShipmentRecovery(button.dataset.recoverShipment)));
}

async function openShipmentRecovery(shipmentID) {
  const shipment = state.exceptionShipments.find((item) => item.id === shipmentID);
  if (!shipment) return toast("异常记录已变化，请刷新后重试", true);
  await openFulfillment(shipment.parent_order_sn, shipment);
}

function renderShipments() {
  const items = state.shipments;
  const counts = state.shipmentMeta.status_counts || {};
  const rows = $("#shipment-rows");
  rows.innerHTML = items.map((shipment) => shipmentRow(shipment)).join("");
  $("#shipments-empty").hidden = items.length > 0;
  $("#metric-ledger-total").textContent = state.shipmentMeta.total ?? items.length;
  $("#metric-ledger-processing").textContent = (counts.submitting || 0) + (counts.label_pending || 0) + (counts.label_ready || 0);
  $("#metric-ledger-exceptions").textContent = (counts.submission_unknown || 0) + (counts.label_failed || 0) + (counts.confirm_failed || 0);
  renderPager("#ledger-pager", state.shipmentMeta, "ledger", () => loadShipmentQueue("ledger"));
}

const omsPlatformOrderStatusMeta = Object.freeze({
  0: { label: "待处理", caption: "等待领星匹配并推进订单", tone: "pending" },
  1: { label: "待获取平台面单", caption: "等待领星获取平台面单", tone: "pending" },
  2: { label: "处理中", caption: "领星已确认，校验通过后自动归档", tone: "" },
  3: { label: "已发货", caption: "领星已发货，校验通过后自动归档", tone: "" },
  missing: { label: "领星无匹配订单", caption: "Temu 已确认发货，但领星两个账户均未检索到同号平台订单", tone: "failed", countKey: -1 },
});

async function loadOMSPlatformOrders() {
  const status = state.omsPlatformOrderStatus;
  const requestSequence = ++state.omsPlatformOrderRequestSequence;
  try {
    const payload = await api(`/oms-platform-orders?status=${status}&page=${state.pages.omsStatuses}&page_size=${state.pageSize}`);
    if (requestSequence !== state.omsPlatformOrderRequestSequence) return;
    if (adjustEmptyPage(payload.meta, "omsStatuses", loadOMSPlatformOrders)) return;
    state.omsPlatformOrders = payload.data || [];
    state.omsPlatformOrderMeta = payload.meta || {};
    renderOMSPlatformOrders();
  } catch (error) {
    if (requestSequence === state.omsPlatformOrderRequestSequence) toast(error.message, true);
  }
}

function renderOMSPlatformOrders() {
  const status = state.omsPlatformOrderStatus;
  const meta = omsPlatformOrderStatusMeta[status];
  const counts = state.omsPlatformOrderMeta.status_counts || {};
  for (const value of [0, 1, 2, 3, "missing"]) {
    const countKey = omsPlatformOrderStatusMeta[value].countKey ?? value;
    $(`#metric-oms-status-${value}`).textContent = counts[countKey] || 0;
  }
  $("#nav-oms-status-count").textContent = (counts[0] || 0) + (counts[1] || 0) + (counts[-1] || 0);
  const statusPrefix = status === "missing" ? "Temu 已发货" : `状态 ${status}`;
  $("#oms-status-heading").textContent = `${statusPrefix} · ${meta.label}`;
  $("#oms-status-caption").textContent = meta.caption;
  $("#oms-status-total").textContent = `共 ${state.omsPlatformOrderMeta.total ?? state.omsPlatformOrders.length} 条`;
  $$('[data-oms-status]').forEach((button) => {
    const active = button.dataset.omsStatus === String(status);
    button.classList.toggle("active", active);
    button.setAttribute("aria-selected", String(active));
  });
  $("#oms-status-rows").innerHTML = state.omsPlatformOrders.map((order) => {
    const missing = status === "missing";
    const warehouseMatched = order.warehouse_code && order.send_warehouse_code && order.warehouse_code.toUpperCase() === order.send_warehouse_code.toUpperCase();
    const warehouseText = missing ? order.warehouse_code || "买单仓未知" : order.send_warehouse_code || "等待领星分仓";
    const archiveLabel = order.archived ? "已归档" : order.sync_status === "manual_required" ? missing ? "需人工补件" : "需人工处理" : order.sync_status === "verified" ? "待归档" : missing ? "等待领星同步" : "待领星推进";
    const archiveTone = order.archived ? "" : order.sync_status === "manual_required" ? "failed" : "pending";
    const labelReadyDuration = elapsedDuration(order.label_ready_at);
    const fulfillmentLabel = order.automatic_fulfillment ? "系统自动发货" : "非自动发货";
    const fulfillmentDetail = order.automatic_fulfillment
      ? labelReadyDuration === null ? "面单成功时间未记录" : `面单处理成功至今 ${labelReadyDuration}`
      : "";
    return `<tr>
      <td><div class="order-id"><strong>${escapeHtml(order.platform_order_sn)}</strong><small>${escapeHtml(order.tracking_number || "暂无跟踪号")}</small></div></td>
      <td><div class="order-id"><strong>${escapeHtml(order.oms_order_no || "-")}</strong><small>${missing ? "未检索到同号平台订单" : escapeHtml(order.audit_time || "尚未审核")}</small></div></td>
      <td><span class="status-badge neutral">${escapeHtml((order.oms_account || "-").toUpperCase())}</span></td>
      <td><span class="status-badge ${meta.tone}">${escapeHtml(statusPrefix)} · ${escapeHtml(meta.label)}</span></td>
      <td><div class="order-id"><strong>${escapeHtml(warehouseText)}</strong><small>${missing ? "领星无发货仓信息" : warehouseMatched ? "与买单仓一致" : order.send_warehouse_code ? `买单仓 ${escapeHtml(order.warehouse_code)}` : "等待仓库信息"}</small></div></td>
      <td>${escapeHtml(order.tracking_number || "-")}</td>
      <td><div class="order-id"><span class="status-badge ${order.automatic_fulfillment ? "" : "neutral"}">${fulfillmentLabel}</span>${fulfillmentDetail ? `<small>${fulfillmentDetail}</small>` : ""}</div></td>
      <td><span class="status-badge ${archiveTone}">${archiveLabel}</span></td>
      <td>${formatTime(order.queried_at)}</td>
    </tr>`;
  }).join("");
  $("#oms-status-empty").hidden = state.omsPlatformOrders.length > 0;
  renderPager("#oms-status-pager", state.omsPlatformOrderMeta, "omsStatuses", loadOMSPlatformOrders);
}

const splitCarrierMeta = Object.freeze({
  GOFO: { label: "GOFO", policy: "免费 POD", tone: "pod" },
  USPS: { label: "USPS", policy: "无需签名", tone: "standard" },
  FEDEX: { label: "FEDEX", policy: "强制签名", tone: "signature" },
});

function setSplitError(message = "") {
  const element = $("#split-error");
  element.textContent = message;
  element.hidden = !message;
}

function formatSplitMoney(value, currency = "USD") {
  const amount = Number(value);
  if (!Number.isFinite(amount)) return "-";
  try {
    return new Intl.NumberFormat("en-US", { style: "currency", currency, minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(amount);
  } catch (_) {
    return `$${amount.toFixed(2)} ${currency}`;
  }
}

function splitPackageInputValue(value, decimals) {
  const amount = Number(value);
  if (!Number.isFinite(amount) || amount <= 0) return "";
  return amount.toFixed(decimals).replace(/\.?0+$/, "");
}

function splitWarehouseLabel(warehouse) {
  return [warehouse?.warehouse_key, warehouse?.warehouse_name].filter(Boolean).join(" · ") || "-";
}

function invalidateSplitQuote() {
  state.splitQuote = null;
  $("#split-quote-result").hidden = true;
}

async function loadSplitPlan(event) {
  event?.preventDefault();
  const parentOrderSN = $("#split-order-number").value.trim();
  const packageCount = Number($("#split-package-count").value);
  if (!parentOrderSN) {
    setSplitError("请输入 Temu 父订单号");
    $("#split-order-number").focus();
    return;
  }
  if (!Number.isInteger(packageCount) || packageCount < 2 || packageCount > 20) {
    setSplitError("包裹数必须是 2 到 20 之间的整数");
    $("#split-package-count").focus();
    return;
  }

  const previousWarehouse = state.splitPlan?.order?.parent_order_sn === parentOrderSN
    ? $("#split-warehouse-select").value
    : "";
  const sequence = ++state.splitPlanRequestSequence;
  state.splitQuoteRequestSequence += 1;
  const button = $("#split-plan-button");
  setLoading(button, true);
  setSplitError("");
  invalidateSplitQuote();

  try {
    const request = { parent_order_sn: parentOrderSN, package_count: packageCount };
    if (previousWarehouse) request.warehouse_key = previousWarehouse;
    const payload = await api("/shipping/split-plan", {
      method: "POST",
      body: JSON.stringify(request),
      timeoutMs: 45000,
    });
    if (sequence !== state.splitPlanRequestSequence) return;
    state.splitPlan = payload.data;
    state.splitQuote = null;
    renderSplitPlan();
  } catch (error) {
    if (sequence !== state.splitPlanRequestSequence) return;
    state.splitPlan = null;
    state.splitQuote = null;
    $("#split-workspace").hidden = true;
    $("#split-empty").hidden = false;
    setSplitError(error.message);
  } finally {
    if (sequence === state.splitPlanRequestSequence) setLoading(button, false);
  }
}

function renderSplitPlan() {
  const plan = state.splitPlan;
  if (!plan) return;
  const warehouses = plan.warehouses || [];
  const selectedWarehouseKey = plan.selected_warehouse_key || warehouses[0]?.warehouse_key || "";
  const selectedWarehouse = warehouses.find((warehouse) => warehouse.warehouse_key === selectedWarehouseKey);

  $("#split-empty").hidden = true;
  $("#split-workspace").hidden = false;
  $("#split-metric-order").textContent = plan.order?.parent_order_sn || "-";
  $("#split-metric-units").textContent = plan.order?.total_units || 0;
  $("#split-metric-packages").textContent = plan.packages?.length || 0;
  $("#split-metric-warehouse").textContent = splitWarehouseLabel(selectedWarehouse);

  const warehouseSelect = $("#split-warehouse-select");
  warehouseSelect.innerHTML = warehouses.map((warehouse) =>
    `<option value="${escapeHtml(warehouse.warehouse_key)}">${escapeHtml(splitWarehouseLabel(warehouse))}</option>`
  ).join("");
  warehouseSelect.value = selectedWarehouseKey;

  const warnings = plan.warnings || [];
  const warningList = $("#split-warnings");
  warningList.hidden = warnings.length === 0;
  warningList.innerHTML = warnings.map((warning) =>
    `<div><svg><use href="#i-alert"/></svg><span>${escapeHtml(warning)}</span></div>`
  ).join("");

  $("#split-package-grid").innerHTML = (plan.packages || []).map((item, index) => {
    const packageNumber = item.number || index + 1;
    const badge = item.needs_measurement
      ? '<span class="status-badge failed">需实测</span>'
      : '<span class="status-badge pending">规格估算</span>';
    const items = (item.items || []).map((line) => `
      <div class="split-package-item">
        <div><strong>${escapeHtml(line.warehouse_sku || "SKU 待映射")}</strong><small>${escapeHtml(line.product_name || line.order_sn)}</small></div>
        <b>× ${line.quantity}</b>
      </div>`).join("");
    const field = (name, label, value, step, decimals) => `
      <label><span>${label}</span><input type="number" min="${step}" max="10000" step="${step}" value="${splitPackageInputValue(value, decimals)}" data-split-field="${name}" required /></label>`;
    return `<article class="split-package-card" data-split-package-index="${index}">
      <header><div><small>拆分包裹</small><h3>#${packageNumber}</h3></div>${badge}</header>
      <div class="split-package-items">${items}</div>
      <div class="split-package-fields">
        ${field("weight_kg", "重量 kg", item.weight_kg, "0.001", 3)}
        ${field("length_cm", "长 cm", item.length_cm, "0.01", 2)}
        ${field("width_cm", "宽 cm", item.width_cm, "0.01", 2)}
        ${field("height_cm", "高 cm", item.height_cm, "0.01", 2)}
      </div>
    </article>`;
  }).join("");

  invalidateSplitQuote();
}

function selectedSplitCarriers() {
  return $$('input[name="split-carrier"]:checked').map((input) => input.value);
}

function collectSplitPackages() {
  const planPackages = state.splitPlan?.packages || [];
  return $$(".split-package-card").map((card) => {
    const index = Number(card.dataset.splitPackageIndex);
    const source = planPackages[index];
    const values = {};
    card.querySelectorAll("[data-split-field]").forEach((input) => {
      if (!input.checkValidity()) {
        input.reportValidity();
        throw new Error(`包裹 #${source?.number || index + 1} 的重量和尺寸必须填写为正数`);
      }
      values[input.dataset.splitField] = Number(input.value);
    });
    return {
      number: source?.number || index + 1,
      items: (source?.items || []).map((item) => ({ order_sn: item.order_sn, quantity: item.quantity })),
      ...values,
    };
  });
}

async function quoteSplitPlan() {
  const plan = state.splitPlan;
  if (!plan) {
    setSplitError("请先生成拆包方案");
    return;
  }
  const carriers = selectedSplitCarriers();
  if (carriers.length === 0) {
    setSplitError("至少选择一家快递商");
    return;
  }
  const warehouseKey = $("#split-warehouse-select").value;
  if (!warehouseKey) {
    setSplitError("请选择发货仓库");
    return;
  }

  let packages;
  try {
    packages = collectSplitPackages();
  } catch (error) {
    setSplitError(error.message);
    return;
  }

  const sequence = ++state.splitQuoteRequestSequence;
  const button = $("#split-quote-button");
  setLoading(button, true);
  setSplitError("");
  invalidateSplitQuote();

  try {
    const payload = await api("/shipping/split-quotes", {
      method: "POST",
      body: JSON.stringify({
        parent_order_sn: plan.order.parent_order_sn,
        warehouse_key: warehouseKey,
        carriers,
        packages,
      }),
      timeoutMs: 60000,
    });
    if (sequence !== state.splitQuoteRequestSequence) return;
    state.splitQuote = payload.data;
    renderSplitQuote();
  } catch (error) {
    if (sequence !== state.splitQuoteRequestSequence) return;
    setSplitError(error.message);
  } finally {
    if (sequence === state.splitQuoteRequestSequence) setLoading(button, false);
  }
}

function splitQuoteCell(code, quote, currency) {
  if (!quote) return '<td><span class="split-not-selected">未参与</span></td>';
  if (!quote.available) {
    return `<td><div class="split-price-cell unavailable"><strong>不可用</strong><small title="${escapeHtml(quote.unavailable_reason || "")}">${escapeHtml(quote.unavailable_reason || "当前包裹不支持")}</small></div></td>`;
  }
  const service = [quote.shipping_company_name, quote.ship_logistics_type].filter(Boolean).join(" · ") || code;
  const marker = quote.signature_required ? "签名" : quote.proof_of_delivery_included ? "POD" : "无签名";
  return `<td><div class="split-price-cell"><strong>${formatSplitMoney(quote.amount, quote.currency || currency)}</strong><small>${escapeHtml(service)}</small><span class="split-service-tag ${splitCarrierMeta[code]?.tone || ""}">${marker}</span></div></td>`;
}

function renderSplitQuote() {
  const result = state.splitQuote;
  if (!result) return;
  const currency = result.currency || "USD";
  const packages = result.packages || [];

  $("#split-quote-result").hidden = false;
  $("#split-mixed-total").textContent = result.mixed_total == null ? "无完整方案" : formatSplitMoney(result.mixed_total, currency);
  $("#split-quote-warehouse").textContent = splitWarehouseLabel(result.warehouse);
  $("#split-quoted-packages").textContent = packages.length;
  $("#split-quote-currency").textContent = currency;
  $("#split-quote-time").textContent = `查询于 ${formatTime(result.queried_at)}`;

  $("#split-carrier-totals").innerHTML = (result.carrier_totals || []).map((total) => {
    const meta = splitCarrierMeta[total.carrier_code] || { label: total.carrier_code, policy: "" };
    const missing = total.unavailable_packages || [];
    const detail = total.available
      ? `全部 ${packages.length} 个包裹可用`
      : `包裹 ${missing.map((number) => `#${number}`).join("、")} 不可用`;
    return `<article class="split-carrier-total ${total.available ? "" : "unavailable"}">
      <div><span>${escapeHtml(meta.policy)}</span><strong>${escapeHtml(meta.label)}</strong></div>
      <div><strong>${total.available && total.amount != null ? formatSplitMoney(total.amount, total.currency || currency) : "无法整单承运"}</strong><small>${escapeHtml(detail)}</small></div>
    </article>`;
  }).join("");

  $("#split-quote-rows").innerHTML = packages.map((item, index) => {
    const packageValue = item.package || {};
    const packageNumber = packageValue.number || index + 1;
    const carrierQuotes = new Map((item.carriers || []).map((quote) => [quote.carrier_code, quote]));
    const dimensions = [packageValue.length_cm, packageValue.width_cm, packageValue.height_cm]
      .map((value) => splitPackageInputValue(value, 2) || "-")
      .join(" × ");
    const recommended = item.recommended_amount == null
      ? '<div class="split-recommended unavailable"><strong>无可用渠道</strong></div>'
      : `<div class="split-recommended"><strong>${formatSplitMoney(item.recommended_amount, currency)}</strong><small>${escapeHtml(item.recommended_carrier)}</small></div>`;
    return `<tr>
      <td><div class="split-package-summary"><strong>包裹 #${packageNumber}</strong><small>${splitPackageInputValue(packageValue.weight_kg, 3)} kg · ${dimensions} cm</small></div></td>
      ${splitQuoteCell("GOFO", carrierQuotes.get("GOFO"), currency)}
      ${splitQuoteCell("USPS", carrierQuotes.get("USPS"), currency)}
      ${splitQuoteCell("FEDEX", carrierQuotes.get("FEDEX"), currency)}
      <td>${recommended}</td>
    </tr>`;
  }).join("");
}
function requireOperationKey(purpose) {
  if (state.operationKey) return Promise.resolve(state.operationKey);
  $("#key-purpose").textContent = purpose; $("#operation-key").value = ""; $("#key-dialog").showModal();
  return new Promise((resolve) => { state.keyResolver = resolve; });
}
function forgetOperationKey() { state.operationKey = ""; sessionStorage.removeItem(operationKeyStorage()); }

function switchView(view) {
  $$(".view").forEach((element) => element.classList.toggle("active", element.id === `view-${view}`));
  $$(".nav-button").forEach((button) => button.classList.toggle("active", button.dataset.view === view));
  $("#crumb-current").textContent = ({ orders: "待发货订单", combined: "可合并订单", "split-quotes": "拆包询价", labels: "自动处理中", exceptions: "自动发货异常", manual: "人工订单", ledger: "自动发货账本", "oms-statuses": "领星订单状态", shipments: "发货记录", warehouses: "仓库映射", "sku-warehouses": "SKU 发货仓库", "tracking-statuses": "物流状态助手" })[view];
  $("#sidebar").classList.remove("open"); $("#backdrop").classList.remove("visible");
  if (view === "manual") loadManualOrders();
  if (view === "combined") loadCombinedShipmentCandidates();
  if (view === "labels") loadShipmentQueue("labels");
  if (view === "exceptions") loadShipmentQueue("exceptions");
  if (view === "ledger") loadShipmentQueue("ledger");
  if (view === "oms-statuses") loadOMSPlatformOrders();
  if (view === "shipments") loadHistory();
  if (view === "warehouses") loadWarehouses();
  if (view === "sku-warehouses") loadSKUWarehouseRules();
  if (view === "tracking-statuses") renderTrackingStatusMappings($("#tracking-status-search").value);
}

$$('.nav-button').forEach((button) => button.addEventListener("click", () => switchView(button.dataset.view)));
$("#split-plan-form").addEventListener("submit", loadSplitPlan);
$("#split-quote-button").addEventListener("click", quoteSplitPlan);
$("#split-warehouse-select").addEventListener("change", (event) => {
  const selected = state.splitPlan?.warehouses?.find((warehouse) => warehouse.warehouse_key === event.target.value);
  $("#split-metric-warehouse").textContent = splitWarehouseLabel(selected);
  setSplitError("");
  invalidateSplitQuote();
});
$("#split-package-grid").addEventListener("input", () => {
  setSplitError("");
  invalidateSplitQuote();
});
$$('input[name="split-carrier"]').forEach((input) => input.addEventListener("change", () => {
  setSplitError("");
  invalidateSplitQuote();
}));
$("#menu-button").addEventListener("click", () => { $("#sidebar").classList.add("open"); $("#backdrop").classList.add("visible"); });
$("#backdrop").addEventListener("click", () => { $("#sidebar").classList.remove("open"); $("#backdrop").classList.remove("visible"); });
$("#sync-orders").addEventListener("click", syncOrders);
$("#bulk-fulfill").addEventListener("click", startBulkFulfillment);
$("#refresh-combined").addEventListener("click", loadCombinedShipmentCandidates);
$("#restart-bulk").addEventListener("click", restartBulkFulfillment);
$("#refresh-manual").addEventListener("click", () => loadManualOrders());
$("#export-manual").addEventListener("click", exportManualOrders);
$("#refresh-warehouse-preview").addEventListener("click", () => loadWarehousePreview());
$("#retry-quote").addEventListener("click", () => createQuote().catch(() => {}));
$("#fulfillment-dialog").addEventListener("close", () => { clearTimeout(state.quoteTimer); state.warehouseController?.abort(); state.quoteController?.abort(); state.quoteSequence += 1; state.recoveryShipment = null; $("#purchase-button").textContent = "确认并购买面单"; });
$("#open-warehouse-mappings").addEventListener("click", () => { $("#fulfillment-dialog").close(); switchView("warehouses"); loadWarehouses(); });
$("#quote-form").addEventListener("submit", (event) => { event.preventDefault(); clearTimeout(state.quoteTimer); createQuote().catch(() => {}); });
$("#purchase-button").addEventListener("click", purchaseLabel);
$("#manual-status").addEventListener("change", () => { state.pages.manual = 1; loadManualOrders(); });
$("#manual-search").addEventListener("input", () => { state.pages.manual = 1; clearTimeout(state.manualSearchTimer); state.manualSearchTimer = setTimeout(loadManualOrders, 250); });
$("#refresh-labels").addEventListener("click", () => loadShipmentQueue("labels"));
$("#refresh-exceptions").addEventListener("click", () => loadShipmentQueue("exceptions"));
$("#refresh-ledger").addEventListener("click", () => loadShipmentQueue("ledger"));
$("#refresh-oms-statuses").addEventListener("click", loadOMSPlatformOrders);
$$('[data-oms-status]').forEach((button) => button.addEventListener("click", () => {
  state.omsPlatformOrderStatus = button.dataset.omsStatus === "missing" ? "missing" : Number(button.dataset.omsStatus);
  state.pages.omsStatuses = 1;
  loadOMSPlatformOrders();
}));
$("#export-ledger-po").addEventListener("click", exportShipmentPO);
$("#refresh-shipments").addEventListener("click", loadHistory);
$("#sync-warehouses").addEventListener("click", async () => { const button = $("#sync-warehouses"); setLoading(button, true); await loadWarehouses(true); setLoading(button, false); });
$("#order-search").addEventListener("input", () => { state.pages.orders = 1; clearTimeout(state.searchTimer); state.searchTimer = setTimeout(loadOrders, 250); });
$("#refresh-sku-rules").addEventListener("click", loadSKUWarehouseRules);
$("#sku-rule-search").addEventListener("input", () => { state.pages.skuRules = 1; clearTimeout(state.skuRuleSearchTimer); state.skuRuleSearchTimer = setTimeout(loadSKUWarehouseRules, 250); });
$("#tracking-status-search").addEventListener("input", (event) => renderTrackingStatusMappings(event.target.value));
$("#key-form").addEventListener("submit", (event) => { event.preventDefault(); state.operationKey = $("#operation-key").value; sessionStorage.setItem(operationKeyStorage(), state.operationKey); $("#key-dialog").close(); state.keyResolver?.(state.operationKey); state.keyResolver = null; });
$("#key-cancel").addEventListener("click", () => { $("#key-dialog").close(); state.keyResolver?.(""); state.keyResolver = null; });
$("#shop-select").addEventListener("change", (event) => {
  sessionStorage.setItem("temu_selected_shop", event.target.value);
  const url = new URL(window.location.href);
  url.searchParams.set("shop", event.target.value);
  window.location.assign(url);
});

async function initialize() {
  renderTrackingStatusMappings();
  await loadShops();
  await Promise.all([checkHealth(), loadToken(), loadOrders(), loadManualOrders(), loadWarehouses(), loadShipments(), loadBulkFulfillment(), loadOMSPlatformOrders()]);
}

void initialize();
setInterval(() => { loadOrders(); loadShipments(); loadBulkFulfillment(); if ($("#view-oms-statuses").classList.contains("active")) loadOMSPlatformOrders(); }, 5000);
setInterval(() => { loadToken(); loadManualOrders(); }, 60000);
