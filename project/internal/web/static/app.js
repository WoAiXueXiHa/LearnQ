const api = "/api/v1";
const $ = selector => document.querySelector(selector);
const $$ = selector => [...document.querySelectorAll(selector)];
let lastRecordBody = null;
let taskTimer = null;
let documentTimer = null;
let imageTimer = null;
let imagePreviewURL = null;
let imagePollFailures = 0;
let taskRequestController = null;
let ragRequestController = null;
let taskRequestVersion = 0;
let ragRequestVersion = 0;
let knowledgeVersion = 0;
let currentTaskID = null;
let currentView = "records";
let taskPollFailures = 0;
let documentPollFailures = 0;
let healthDependencies = {};
let ragReady = false;
let healthInFlight = false;

const escapeHTML = value => String(value ?? "").replace(/[&<>"']/g, char => ({
  "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
}[char]));

function showNotice(message, kind = "info") {
  const box = $("#notice");
  box.hidden = false;
  box.className = `notice ${kind}`;
  box.textContent = message;
  clearTimeout(showNotice.timer);
  showNotice.timer = setTimeout(() => box.hidden = true, 6000);
}

function updateModelMode(mode, modelName = "") {
  const badge = $("#modelMode");
  if (!badge) return;
  const normalizedMode = String(mode || "").toLowerCase();
  const normalizedModel = String(modelName || "");
  const fake = normalizedMode === "fake" || normalizedModel.toLowerCase().includes("fake");
  if (fake) {
    badge.textContent = `Fake 演示模式${normalizedModel ? ` · ${normalizedModel}` : ""}`;
    badge.className = "badge warning";
    badge.title = "当前结果用于验证流程，不能代表真实模型质量。";
    return;
  }
  if (normalizedMode === "real" || normalizedModel) {
    badge.textContent = `正式模型${normalizedModel ? ` · ${normalizedModel}` : ""}`;
    badge.className = "badge success";
    badge.title = "模型名称来自运行时信息或最近一次任务 Trace。";
    return;
  }
  badge.textContent = "模型模式：以任务 Trace 为准";
  badge.className = "badge";
}

function setButtonBusy(button, busy, busyText = "处理中…") {
  if (!button) return;
  if (busy) {
    button.dataset.originalText = button.textContent;
    button.disabled = true;
    button.textContent = busyText;
  } else {
    button.disabled = false;
    button.textContent = button.dataset.originalText || button.textContent;
    delete button.dataset.originalText;
  }
}

class APIError extends Error {
  constructor(message, status = 0, code = "NETWORK_ERROR", details = null) {
    super(message);
    this.name = "APIError";
    this.status = status;
    this.code = code;
    this.details = details;
  }
}

async function fetchData(url, options = {}, timeoutMs = 15000) {
  const controller = new AbortController();
  let timedOut = false;
  const externalSignal = options.signal;
  const forwardAbort = () => controller.abort();
  if (externalSignal) {
    if (externalSignal.aborted) controller.abort();
    else externalSignal.addEventListener("abort", forwardAbort, {once: true});
  }
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs);
  try {
    const response = await fetch(url, {...options, signal: controller.signal});
    const contentType = response.headers.get("content-type") || "";
    const payload = contentType.includes("json") ? await response.json() : await response.text();
    if (!response.ok) {
      const error = payload?.error;
      throw new APIError(
        error?.message ? `${error.message}（${error.code}）` : String(payload),
        response.status,
        error?.code || `HTTP_${response.status}`,
        error?.details
      );
    }
    return payload.data;
  } catch (error) {
    if (error.name === "AbortError") {
      throw new APIError(timedOut ? "请求超时，请稍后重试。" : "请求已取消。", 0, timedOut ? "REQUEST_TIMEOUT" : "REQUEST_CANCELLED");
    }
    if (error instanceof APIError) throw error;
    throw new APIError(error.message || "网络请求失败。");
  } finally {
    clearTimeout(timer);
    externalSignal?.removeEventListener("abort", forwardAbort);
  }
}

function request(path, options = {}, timeoutMs = 15000) {
  return fetchData(api + path, options, timeoutMs);
}

function retryable(error) {
  return error.code !== "REQUEST_CANCELLED" &&
    (error.status === 0 || error.status === 408 || error.status === 429 || error.status >= 500);
}

function switchView(id, scroll = true) {
  const target = document.getElementById(id);
  if (!target) return;
  currentView = id;
  if (id !== "task") {
    clearTimeout(taskTimer);
    taskRequestController?.abort();
  }
  if (id !== "knowledge") {
    clearTimeout(documentTimer);
    clearTimeout(imageTimer);
    ragRequestController?.abort();
  }
  $$("main > section").forEach(section => section.hidden = section.id !== id);
  $$(".tabs button").forEach(button => {
    const active = button.dataset.view === id;
    button.classList.toggle("active", active);
    button.setAttribute("aria-selected", String(active));
    if (active) button.scrollIntoView({behavior: "smooth", block: "nearest", inline: "nearest"});
  });
  if (scroll) target.scrollIntoView({behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth", block: "start"});
  if (id === "records") loadRecords();
  if (id === "task" && currentTaskID) openTask(currentTaskID);
  if (id === "reviews") loadReviews();
  if (id === "knowledge") {
    loadDocuments();
    loadImages();
  }
}

$$(".tabs button").forEach(button => button.onclick = () => switchView(button.dataset.view));

async function health() {
  if (healthInFlight) return;
  healthInFlight = true;
  try {
    const data = await fetchData("/health/ready", {cache: "no-store"}, 5000);
    const runtime = data?.runtime || data?.ai || {};
    updateModelMode(data?.ai_mode || runtime.mode, data?.chat_model || runtime.chat_model);
    healthDependencies = data?.dependencies || {};
    $("#health").textContent = "服务正常";
    $("#health").className = "badge success";
    $("#health").title = JSON.stringify(healthDependencies);
  } catch (error) {
    const dependencies = error.details?.dependencies || {};
    if (Object.keys(dependencies).length) healthDependencies = dependencies;
    if (!Object.keys(healthDependencies).length) healthDependencies = {api: "unavailable"};
    const failed = Object.entries(healthDependencies).filter(([, value]) => value === "unavailable").map(([name]) => name);
    $("#health").textContent = failed.length ? `${failed.join(" / ")} 异常` : error.message || "服务不可用";
    $("#health").className = "badge danger";
  } finally {
    healthInFlight = false;
  }
}

function recordPayload(forceCreate = false) {
  const form = new FormData($("#recordForm"));
  return {
    title: form.get("title").trim(),
    summary: form.get("summary").trim(),
    duration_minutes: Number(form.get("duration")),
    force_create: forceCreate,
    modules: [{category: form.get("category"), content: form.get("content").trim()}]
  };
}

async function submitRecord(forceCreate = false) {
  const button = forceCreate ? $("#forceCreate") : $("#recordSubmit");
  setButtonBusy(button, true, "正在创建…");
  $("#recordSubmit").disabled = true;
  $("#forceCreate").disabled = true;
  try {
    const body = recordPayload(forceCreate);
    lastRecordBody = body;
    const bodyText = JSON.stringify(body);
    let operation;
    try { operation = JSON.parse(sessionStorage.getItem("learnq-record-operation") || "null"); } catch { operation = null; }
    if (!operation || operation.body !== bodyText) operation = {key: crypto.randomUUID(), body: bodyText};
    sessionStorage.setItem("learnq-record-operation", JSON.stringify(operation));
    const data = await request("/study-records", {
      method: "POST",
      headers: {"Content-Type": "application/json", "Idempotency-Key": operation.key},
      body: bodyText
    });
    $("#duplicateBox").hidden = !data.deduplicated;
    sessionStorage.removeItem("learnq-record-operation");
    showNotice(data.deduplicated ? "发现相同记录，已打开原任务。" : "学习记录已创建，正在生成报告。", data.deduplicated ? "warning" : "success");
    await loadRecords();
    if (data.deduplicated && !forceCreate) {
      $("#duplicateBox").scrollIntoView({behavior: "smooth"});
    } else {
      currentTaskID = Number(data.task_id);
      $("#taskID").value = data.task_id;
      switchView("task");
    }
  } catch (error) {
    if (error.status >= 400 && error.status < 500) sessionStorage.removeItem("learnq-record-operation");
    showNotice(error.message, "danger");
  } finally {
    setButtonBusy(button, false);
    $("#recordSubmit").disabled = false;
    $("#forceCreate").disabled = false;
  }
}

$("#recordForm").onsubmit = event => {
  event.preventDefault();
  submitRecord(false);
};
$("#forceCreate").onclick = () => submitRecord(true);

async function loadRecords() {
  try {
    const records = await request("/study-records");
    $("#recordList").innerHTML = records.length ? records.map(record => `
      <article class="card clickable" role="button" tabindex="0" onclick="openTaskFromRecord(${record.task_id})" onkeydown="if(event.key==='Enter'||event.key===' '){event.preventDefault();openTaskFromRecord(${record.task_id})}">
        <div class="card-title"><strong>${escapeHTML(record.title)}</strong><span class="status ${record.task_status}">${escapeHTML(record.task_status || "未知")}</span></div>
        <p>${escapeHTML(record.summary || "未填写摘要")}</p>
        <small>${record.duration_minutes} 分钟 · ${new Date(record.created_at).toLocaleString()} · Task #${record.task_id}</small>
      </article>`).join("") : `<div class="empty">还没有学习记录。</div>`;
    localizeStatuses($("#recordList"));
  } catch (error) {
    $("#recordList").innerHTML = `<div class="empty danger">${escapeHTML(error.message)}</div>`;
  }
}

function openTaskFromRecord(id) {
  currentTaskID = Number(id);
  $("#taskID").value = id;
  switchView("task");
}

function renderMarkdown(markdown) {
  const lines = escapeHTML(markdown || "").split("\n");
  const output = [];
  let paragraph = [];
  let inCode = false;
  let listTag = null;
  const flushParagraph = () => {
    if (paragraph.length) output.push(`<p>${paragraph.join("<br>")}</p>`);
    paragraph = [];
  };
  const closeList = () => {
    if (listTag) output.push(`</${listTag}>`);
    listTag = null;
  };
  for (const line of lines) {
    if (line.startsWith("```")) {
      flushParagraph();
      closeList();
      output.push(inCode ? "</code></pre>" : "<pre><code>");
      inCode = !inCode;
      continue;
    }
    if (inCode) {
      output.push(`${line}\n`);
      continue;
    }
    const heading = line.match(/^(#{1,3})\s+(.+)$/);
    if (heading) {
      flushParagraph();
      closeList();
      const level = Number(heading[1].length) + 1;
      output.push(`<h${level}>${heading[2]}</h${level}>`);
      continue;
    }
    const item = line.match(/^[-*]\s+(.+)$/);
    const orderedItem = line.match(/^\d+[.)]\s+(.+)$/);
    if (item || orderedItem) {
      flushParagraph();
      const wantedTag = orderedItem ? "ol" : "ul";
      if (listTag && listTag !== wantedTag) closeList();
      if (!listTag) output.push(`<${wantedTag}>`);
      listTag = wantedTag;
      output.push(`<li>${(item || orderedItem)[1]}</li>`);
      continue;
    }
    if (!line.trim()) {
      flushParagraph();
      closeList();
      continue;
    }
    closeList();
    paragraph.push(line);
  }
  flushParagraph();
  closeList();
  if (inCode) output.push("</code></pre>");
  return output.join("");
}

function renderTrace(trace) {
  const runs = trace?.agent_runs || [];
  const tools = trace?.tool_calls || [];
  const latestModel = runs.find(run => run?.model_name)?.model_name;
  if (latestModel) updateModelMode("", latestModel);
  return `<div class="card"><h3>Agent 执行轨迹</h3>
    ${runs.length ? runs.map(run => `<div class="trace-row"><strong>${escapeHTML(run.skill_name)}</strong><span>${escapeHTML(run.model_name)}</span><small>${run.latency_ms} ms · 输入 ${run.input_tokens} / 输出 ${run.output_tokens} tokens</small></div>`).join("") : `<p class="muted">Agent 尚未运行。</p>`}
    ${tools.length ? `<h4>真实工具调用</h4>${tools.map(tool => `<details><summary>${escapeHTML(tool.tool_name)} · ${tool.latency_ms} ms ${tool.error_reason ? "· 失败" : ""}</summary><pre>${escapeHTML(tool.response_json)}</pre>${tool.retrieval_citations_json !== "[]" ? `<pre>${escapeHTML(tool.retrieval_citations_json)}</pre>` : ""}</details>`).join("")}` : ""}
  </div>`;
}

const taskStatusText = {
  pending: "等待入队", queued: "已入队", processing: "处理中",
  retry_wait: "等待重试", succeeded: "已完成", dead: "失败终止"
};

const documentStatusText = {
  uploaded: "已上传",
  parsing: "解析中",
  embedding: "生成向量中",
  indexing: "写入索引中",
  ready: "可查询",
  failed: "索引失败",
  deleting: "删除中",
  deleted: "已删除"
};

const imageStatusText = {
  uploaded: "已上传",
  pending: "等待分析",
  queued: "等待分析",
  processing: "分析中",
  analyzing: "分析中",
  ready: "分析完成",
  failed: "分析失败",
  indexing: "加入知识库中",
  deleting: "删除中"
};

function renderStringList(heading, value) {
  const items = Array.isArray(value) ? value.filter(Boolean) : value ? [value] : [];
  if (!items.length) return "";
  return `<section class="description-section"><h4>${escapeHTML(heading)}</h4><ul>${items.map(item => `<li>${escapeHTML(item)}</li>`).join("")}</ul></section>`;
}

const exportStatusText = {
  pending: "等待导出",
  succeeded: "导出成功",
  failed: "导出失败"
};

function localizeStatuses(root) {
  if (!root) return;
  root.querySelectorAll(".status").forEach(element => {
    const status = element.textContent.trim();
    element.textContent = taskStatusText[status] || documentStatusText[status] || status;
  });
  root.querySelectorAll(".badge").forEach(element => {
    const match = element.textContent.trim().match(/^导出\s+([a-z_]+)$/);
    if (!match) return;
    element.textContent = exportStatusText[match[1]] || element.textContent;
  });
}

function renderTimeline(task) {
  const stages = task.status === "retry_wait" ?
    ["pending", "queued", "processing", "retry_wait"] :
    ["pending", "queued", "processing", "succeeded"];
  const current = stages.indexOf(task.status);
  return `<div class="timeline">${stages.map((stage, index) => {
    const state = task.status === "dead" && stage === "processing" ? "failed-step" :
      index < current ? "done" : index === current ? "active" : "";
    return `<span class="${state}">${taskStatusText[stage]}</span>`;
  }).join("")}</div>`;
}

function renderTaskProduct(data) {
  const {task, report, review_task: review, document, image} = data;
  if (task.kind === "image_describe") {
    if (!image) {
      return `<div class="card ${task.status === "dead" ? "warning" : "danger"}">${task.status === "dead" ? "图片已删除，分析任务已取消。" : "分析任务找不到对应图片。"}</div>`;
    }
    const description = image.description || {};
    return `<article class="card report">
      <div class="card-title"><h3>${escapeHTML(description.title || "图片分析")}</h3><span class="status ${escapeHTML(image.status)}">${escapeHTML(imageStatusText[image.status] || image.status)}</span></div>
      <p>${escapeHTML(description.summary || (task.status === "dead" ? "图片分析失败。" : "图片正在分析。"))}</p>
      ${description.learning_explanation ? `<div class="markdown">${renderMarkdown(description.learning_explanation)}</div>` : ""}
      ${description.extracted_text ? `<details><summary>查看提取文字</summary><pre>${escapeHTML(description.extracted_text)}</pre></details>` : ""}
      ${renderStringList("关键点", description.key_points)}
      ${renderStringList("不确定项", description.uncertainties)}
      ${image.last_error ? `<p class="danger">${escapeHTML(image.last_error)}</p>` : ""}
      <div class="actions">
        ${image.status === "ready" && !image.derived_document_id ? `<button type="button" data-image-action="index" data-image-id="${image.id}">加入知识库</button>` : ""}
        ${image.derived_document_id ? `<span class="badge success">已加入知识库 · Document #${image.derived_document_id}</span>` : ""}
        ${image.status === "failed" ? `<button class="secondary" type="button" data-image-action="retry" data-image-id="${image.id}">重试分析</button>` : ""}
      </div>
    </article>`;
  }
  if (task.kind === "document_index") {
    if (!document) {
      return `<div class="card ${task.status === "dead" ? "warning" : "danger"}">${task.status === "dead" ? "文档已删除，索引任务已取消。" : "索引任务找不到对应文档。"}</div>`;
    }
    return `<article class="card">
      <div class="card-title"><h3>文档索引</h3><span class="status ${document.status}">${escapeHTML(document.status)}</span></div>
      <p>${escapeHTML(document.filename)}</p>
      <small>Document #${document.id} · Task #${task.id}</small>
      ${document.status === "ready" ? `<p class="success-text">文档已经可以用于 RAG 查询。</p>` : ""}
      ${document.error_message ? `<p class="danger">${escapeHTML(document.error_message)}</p>` : ""}
    </article>`;
  }
  if (report) {
    return `<article class="card report">
      <div class="card-title"><h3>生成报告</h3><span class="badge ${report.export_status === "succeeded" ? "success" : "warning"}">导出 ${escapeHTML(report.export_status)}</span></div>
      <div class="markdown">${renderMarkdown(report.markdown_content)}</div>
      <div class="actions"><a class="button-link secondary" href="${api}/reports/${report.id}/report.md" target="_blank" rel="noopener">查看 Markdown</a></div>
      ${report.export_error ? `<p class="danger">${escapeHTML(report.export_error)}</p>` : ""}
    </article>
    ${review ? `<article class="card"><h3>复习安排</h3><p>下一次：${new Date(review.due_at).toLocaleString()}</p><small>当前熟练度 ${review.mastery}/5</small></article>` : ""}
    ${renderTrace(data.trace)}`;
  }
  if (["succeeded", "dead"].includes(task.status)) {
    return `<div class="card danger">任务已经结束，但预期报告不存在。请查看错误信息或重新执行任务。</div>${renderTrace(data.trace)}`;
  }
  return `<div class="card empty">报告生成中，页面会自动刷新。</div>${renderTrace(data.trace)}`;
}

async function openTask(id) {
  const numericID = Number(id);
  if (!Number.isInteger(numericID) || numericID <= 0) {
    showNotice("Task ID 必须是正整数。", "warning");
    return;
  }
  clearTimeout(taskTimer);
  taskRequestController?.abort();
  taskRequestController = new AbortController();
  const requestVersion = ++taskRequestVersion;
  currentTaskID = numericID;
  $("#taskID").value = numericID;
  try {
    const data = await request(`/tasks/${numericID}/detail`, {cache: "no-store", signal: taskRequestController.signal});
    if (requestVersion !== taskRequestVersion || currentTaskID !== numericID) return;
    const task = data.task;
    const record = data.study_record;
    const terminal = ["succeeded", "dead"].includes(task.status);
    const stallThreshold = task.status === "processing" ? 50000 : 15000;
    const stalled = !terminal && Date.now() - new Date(task.updated_at).getTime() > stallThreshold;
    const workerUnavailable = healthDependencies.worker === "unavailable";
    $("#taskDetail").className = "stack";
    $("#taskDetail").querySelector(".poll-warning")?.remove();
    $("#taskDetail").innerHTML = `
      <article class="card">
        <div class="card-title"><h3>${escapeHTML(record?.title || data.document?.filename || task.kind)}</h3><span class="status ${task.status}">${escapeHTML(taskStatusText[task.status] || task.status)}</span></div>
        <p>${escapeHTML(record?.summary || (task.kind === "image_describe" ? "图片视觉理解任务" : task.kind === "document_index" ? "文档索引任务" : "任务处理中"))}</p>
        ${renderTimeline(task)}
        ${workerUnavailable ? `<p class="danger">Worker 未运行，任务暂时无法继续处理。</p>` : ""}
        ${stalled && !workerUnavailable ? `<p class="warning-text">任务超过预期时间没有推进，正在降低刷新频率；请检查 Worker 或模型调用日志。</p>` : ""}
        ${task.last_error ? `<p class="danger">${escapeHTML(task.last_error)}</p>` : ""}
        ${task.status === "dead" && data.document ? `<button onclick="retryTask(${task.id}, this)">重新执行</button>` : task.status === "dead" && task.kind === "study_report" ? `<button onclick="retryTask(${task.id}, this)">重新执行</button>` : ""}
      </article>
      ${renderTaskProduct(data)}`;
    localizeStatuses($("#taskDetail"));
    taskPollFailures = 0;
    if (!terminal && currentView === "task") taskTimer = setTimeout(() => openTask(numericID), stalled ? 5000 : 1000);
  } catch (error) {
    if (error.code === "REQUEST_CANCELLED" || requestVersion !== taskRequestVersion || currentTaskID !== numericID) return;
    taskPollFailures += 1;
    if (retryable(error) && currentView === "task") {
      const warning = `<div class="card warning poll-warning">刷新失败：${escapeHTML(error.message)} 正在自动重试，已保留上一次结果。</div>`;
      const oldWarning = $("#taskDetail").querySelector(".poll-warning");
      if (oldWarning) oldWarning.outerHTML = warning;
      else $("#taskDetail").insertAdjacentHTML("afterbegin", warning);
      taskTimer = setTimeout(() => openTask(numericID), Math.min(1000 * (2 ** taskPollFailures), 10000));
    } else {
      $("#taskDetail").className = "stack";
      $("#taskDetail").innerHTML = `<div class="card danger">${escapeHTML(error.message)}</div>`;
    }
  }
}

$("#taskLookupForm").onsubmit = event => {
  event.preventDefault();
  openTask($("#taskID").value);
};

async function retryTask(id, button) {
  setButtonBusy(button, true, "正在重试…");
  try {
    await request(`/tasks/${id}/retry`, {method: "POST"});
    showNotice("任务已重新进入队列。", "success");
    openTask(id);
  } catch (error) { showNotice(error.message, "danger"); }
  finally { setButtonBusy(button, false); }
}

function reportPreview(markdown) {
  return String(markdown || "")
    .split("\n")
    .filter(line => !line.includes("_tool_results"))
    .map(line => line.replace(/^#+\s*/, ""))
    .join(" ")
    .replace(/\s+/g, " ")
    .trim()
    .slice(0, 160);
}

function reviewCard(review, due) {
  return `<article class="card">
    <div class="card-title"><strong>${escapeHTML(review.record_title)}</strong><span class="badge ${due ? "danger" : "warning"}">${due ? "已到期" : new Date(review.due_at).toLocaleString()}</span></div>
    <p>${escapeHTML(reportPreview(review.markdown_content))}</p>
    ${due ? `<label>本次掌握程度<select id="mastery-${review.id}">${[0,1,2,3,4,5].map(value => `<option value="${value}">${value}/5</option>`).join("")}</select></label><div class="actions review-actions"><button onclick="completeReview(${review.id}, this)">完成复习</button><button class="secondary" onclick="skipReview(${review.id}, this)">延后一天</button></div>` : `<small>将在 ${relativeTime(review.due_at)}后到期</small>`}
  </article>`;
}

function relativeTime(date) {
  const hours = Math.max(0, Math.round((new Date(date) - Date.now()) / 36e5));
  return hours < 24 ? `${hours} 小时` : `${Math.round(hours / 24)} 天`;
}

async function loadReviews() {
  try {
    let [due, upcoming] = await Promise.all([request("/review-tasks?scope=due"), request("/review-tasks?scope=upcoming")]);
    due = Array.isArray(due) ? due : [];
    upcoming = Array.isArray(upcoming) ? upcoming : [];
    $("#dueList").innerHTML = due.length ? due.map(item => reviewCard(item, true)).join("") : `<div class="empty">现在没有到期复习。</div>`;
    $("#upcomingList").innerHTML = upcoming.length ? upcoming.map(item => reviewCard(item, false)).join("") : `<div class="empty">报告生成后，复习计划会出现在这里。</div>`;
  } catch (error) { showNotice(error.message, "danger"); }
}

async function completeReview(id, button) {
  const mastery = Number($(`#mastery-${id}`).value);
  const buttons = [...button.closest(".review-actions").querySelectorAll("button")];
  buttons.forEach(item => item.disabled = true);
  setButtonBusy(button, true, "正在保存…");
  try {
    const result = await request(`/review-tasks/${id}/complete`, {method: "POST", headers: {"Content-Type": "application/json"}, body: JSON.stringify({mastery})});
    showNotice(`复习已完成，下次时间：${new Date(result.due_at).toLocaleString()}`, "success");
    await loadReviews();
  } catch (error) {
    showNotice(error.message, "danger");
    buttons.forEach(item => item.disabled = false);
    setButtonBusy(button, false);
  }
}
async function skipReview(id, button) {
  const buttons = [...button.closest(".review-actions").querySelectorAll("button")];
  buttons.forEach(item => item.disabled = true);
  setButtonBusy(button, true, "正在延后…");
  try {
    await request(`/review-tasks/${id}/skip`, {method: "POST", headers: {"Content-Type": "application/json"}, body: "{}"});
    showNotice("已延后一天。", "success");
    await loadReviews();
  } catch (error) {
    showNotice(error.message, "danger");
    buttons.forEach(item => item.disabled = false);
    setButtonBusy(button, false);
  }
}

const skillExamples = {
  "daily-review": {title: "Go 并发学习", summary: "学习 goroutine、channel 和 context", duration_minutes: 30},
  "algorithm-diagnosis": {topic: "二分搜索边界", question: "如何避免左右边界错误？"},
  "interview-followup": {topic: "MySQL 事务与 Outbox", summary: "希望生成递进面试追问"},
  "project-explanation": {title: "LearnQ", summary: "异步任务、lease fencing、RAG"},
  "weekly-plan": {title: "本周计划", summary: "请基于 SQL 周统计组织下周行动"},
  "multi-agent": {title: "LearnQ 可靠任务链", summary: "分别观察复盘、面试追问、算法诊断和计划输出", modules: ["algorithm", "project"]}
};

function updateSkillInput() {
  $("#skillInput").value = JSON.stringify(skillExamples[$("#skillName").value] || {}, null, 2);
}

$("#skillForm").onsubmit = async event => {
  event.preventDefault();
  const name = $("#skillName").value;
  const button = event.submitter;
  setButtonBusy(button, true, "正在运行…");
  try {
    JSON.parse($("#skillInput").value);
    const data = await request(`/skills/${name}/runs`, {method: "POST", headers: {"Content-Type": "application/json"}, body: $("#skillInput").value}, 85000);
    if (name === "multi-agent") {
      const workflow = data.workflow;
      const cards = workflow.routes.map(route => {
        const failure = workflow.errors?.[route];
        if (failure) return `<article class="card danger"><h3>${escapeHTML(route)}</h3><p>${escapeHTML(failure)}</p></article>`;
        const response = workflow.outputs?.[route];
        const raw = response?.Content ?? response?.content;
        if (!raw) return `<article class="card warning"><h3>${escapeHTML(route)}</h3><p>该 Agent 没有返回结果。</p></article>`;
        const output = JSON.parse(raw);
        const runID = data.agent_run_ids?.[route];
        return `<article class="card report"><div class="card-title"><h3>${escapeHTML(output.title)}</h3><span class="badge success">${runID ? `Run #${runID}` : "完成"}</span></div><p>${escapeHTML(output.summary)}</p>${output.sections.map(section => `<h4>${escapeHTML(section.heading)}</h4><div class="markdown">${renderMarkdown(section.content)}</div>`).join("")}</article>`;
      }).join("");
      $("#skillResult").className = "stack";
      $("#skillResult").innerHTML = `<div class="card warning"><strong>手动实验：</strong>各 Agent 独立输出，不参与异步学习报告主链。</div>${cards}`;
      return;
    }
    const output = data.output;
    $("#skillResult").className = "stack";
    $("#skillResult").innerHTML = `<article class="card report"><div class="card-title"><h3>${escapeHTML(output.title)}</h3><span class="badge success">Run #${data.agent_run_id}</span></div><p>${escapeHTML(output.summary)}</p>${output.sections.map(section => `<h4>${escapeHTML(section.heading)}</h4><div class="markdown">${renderMarkdown(section.content)}</div>`).join("")}</article>`;
    const detail = await request(`/agent-runs/${data.agent_run_id}`);
    $("#skillResult").insertAdjacentHTML("beforeend", renderTrace({agent_runs: [detail.run], tool_calls: detail.tool_calls}));
  } catch (error) {
    $("#skillResult").innerHTML = `<div class="card danger">${escapeHTML(error.message)}</div>`;
  } finally {
    setButtonBusy(button, false);
  }
};

$("#uploadForm").onsubmit = async event => {
  event.preventDefault();
  const button = event.submitter;
  setButtonBusy(button, true, "正在上传…");
  const form = new FormData();
  form.append("file", $("#documentFile").files[0]);
  try {
    const data = await request("/documents", {method: "POST", body: form}, 30000);
    showNotice(`文档 #${data.document_id} 已上传，正在索引。`, "success");
    loadDocuments(true);
  } catch (error) { showNotice(error.message, "danger"); }
  finally { setButtonBusy(button, false); }
};

function switchMaterial(kind) {
  const image = kind === "image";
  $("#documentUploadPanel").hidden = image;
  $("#imageUploadPanel").hidden = !image;
  $("#materialDocumentTab").classList.toggle("active", !image);
  $("#materialImageTab").classList.toggle("active", image);
  $("#materialDocumentTab").setAttribute("aria-selected", String(!image));
  $("#materialImageTab").setAttribute("aria-selected", String(image));
}

$("#materialDocumentTab").onclick = () => switchMaterial("document");
$("#materialImageTab").onclick = () => switchMaterial("image");
$("#materialRefresh").onclick = () => {
  loadDocuments();
  loadImages(true);
};

function clearImagePreview() {
  if (imagePreviewURL) URL.revokeObjectURL(imagePreviewURL);
  imagePreviewURL = null;
  $("#imagePreview").removeAttribute("src");
  $("#imagePreviewBox").hidden = true;
}

$("#imageFile").onchange = () => {
  clearImagePreview();
  const file = $("#imageFile").files[0];
  if (!file) return;
  const supported = ["image/png", "image/jpeg"];
  if (!supported.includes(file.type)) {
    $("#imageFile").value = "";
    showNotice("仅支持 PNG 或 JPEG 图片。", "warning");
    return;
  }
  if (file.size > 5 * 1024 * 1024) {
    $("#imageFile").value = "";
    showNotice("图片不能超过 5 MiB。", "warning");
    return;
  }
  imagePreviewURL = URL.createObjectURL(file);
  $("#imagePreview").src = imagePreviewURL;
  $("#imagePreview").alt = `${file.name} 的上传预览`;
  $("#imagePreviewName").textContent = file.name;
  $("#imagePreviewMeta").textContent = `${(file.size / 1024).toFixed(1)} KiB`;
  $("#imagePreviewBox").hidden = false;
};

$("#imagePreview").onload = event => {
  const image = event.currentTarget;
  $("#imagePreviewMeta").textContent += ` · ${image.naturalWidth} × ${image.naturalHeight}`;
};

window.addEventListener("beforeunload", clearImagePreview);

$("#imageUploadForm").onsubmit = async event => {
  event.preventDefault();
  const button = event.submitter;
  const file = $("#imageFile").files[0];
  if (!file) return;
  setButtonBusy(button, true, "正在上传…");
  const form = new FormData();
  form.append("file", file);
  form.append("prompt", $("#imagePrompt").value.trim());
  try {
    const data = await request("/images", {method: "POST", body: form}, 30000);
    showNotice(`图片 #${data.image_id || data.id} 已上传，正在分析。`, "success");
    $("#imageUploadForm").reset();
    clearImagePreview();
    await loadImages(true);
  } catch (error) {
    showNotice(error.message, "danger");
  } finally {
    setButtonBusy(button, false);
  }
};

function imageDescriptionMarkup(image) {
  const description = image.description || {};
  if (!description || !Object.keys(description).length) return "";
  return `<div class="image-description">
    ${description.summary ? `<p>${escapeHTML(description.summary)}</p>` : ""}
    ${description.learning_explanation ? `<div class="markdown">${renderMarkdown(description.learning_explanation)}</div>` : ""}
    ${description.extracted_text ? `<details><summary>查看提取文字</summary><pre>${escapeHTML(description.extracted_text)}</pre></details>` : ""}
    ${renderStringList("关键点", description.key_points)}
    ${renderStringList("不确定项", description.uncertainties)}
  </div>`;
}

function imageCard(image) {
  const status = imageStatusText[image.status] || image.status || "未知";
  const active = ["uploaded", "pending", "queued", "processing", "analyzing", "indexing"].includes(image.status);
  return `<article class="card image-card">
    <div class="image-card-layout">
      <img class="image-thumb" src="${api}/images/${image.id}/content" alt="${escapeHTML(image.original_filename || "已上传图片")}" loading="lazy">
      <div>
        <div class="card-title"><strong>${escapeHTML(image.original_filename || `图片 #${image.id}`)}</strong><span class="status ${escapeHTML(image.status)}">${escapeHTML(status)}</span></div>
        <small>${image.width || "?"} × ${image.height || "?"} · ${image.size_bytes ? `${(image.size_bytes / 1024).toFixed(1)} KiB` : "大小未知"} · Image #${image.id}</small>
        ${active ? `<p class="muted">处理完成后会自动更新。</p>` : ""}
        ${imageDescriptionMarkup(image)}
        ${image.last_error ? `<p class="danger">${escapeHTML(image.last_error)}</p>` : ""}
        <div class="actions">
          ${image.status === "ready" && !image.derived_document_id ? `<button type="button" data-image-action="index" data-image-id="${image.id}">加入知识库</button>` : ""}
          ${image.derived_document_id ? `<span class="badge success">已加入知识库 · Document #${image.derived_document_id}</span>` : ""}
          ${image.status === "failed" ? `<button class="secondary" type="button" data-image-action="retry" data-image-id="${image.id}">重试分析</button>` : ""}
          <button class="secondary danger-outline" type="button" data-image-action="delete" data-image-id="${image.id}">删除</button>
        </div>
      </div>
    </div>
  </article>`;
}

async function loadImages(keepPolling = false) {
  clearTimeout(imageTimer);
  try {
    let images = await request("/images", {cache: "no-store"});
    images = Array.isArray(images) ? images : [];
    const active = images.some(image => ["uploaded", "pending", "queued", "processing", "analyzing", "indexing", "deleting"].includes(image.status));
    imagePollFailures = 0;
    $("#imageList").innerHTML = images.length ? images.map(imageCard).join("") : `<div class="empty">还没有图片。可上传代码截图、架构图或学习笔记进行分析。</div>`;
    if ((active || keepPolling) && currentView === "knowledge") imageTimer = setTimeout(() => loadImages(false), 1500);
  } catch (error) {
    imagePollFailures += 1;
    $("#imageList").innerHTML = `<div class="empty danger">图片列表加载失败：${escapeHTML(error.message)} <button class="secondary" type="button" data-image-action="reload">重试</button></div>`;
    if (retryable(error) && currentView === "knowledge") imageTimer = setTimeout(() => loadImages(true), Math.min(1500 * (2 ** imagePollFailures), 10000));
  }
}

async function imageAction(action, id, button) {
  if (action === "reload") return loadImages(true);
  if (action === "delete" && !confirm("确认删除图片、分析结果及其知识库文档和向量索引？")) return;
  if (action === "delete") {
    ragRequestController?.abort();
    knowledgeVersion += 1;
  }
  setButtonBusy(button, true, action === "delete" ? "正在删除…" : action === "index" ? "正在入库…" : "正在提交…");
  try {
    if (action === "delete") await request(`/images/${id}`, {method: "DELETE"});
    if (action === "retry") await request(`/images/${id}/retry`, {method: "POST"});
    if (action === "index") await request(`/images/${id}/index`, {method: "POST"});
    showNotice(action === "delete" ? "图片已删除。" : action === "index" ? "正在将描述加入知识库。" : "图片已重新进入分析队列。", "success");
    if (action === "delete") {
      $("#ragResult").className = "stack empty";
      $("#ragResult").textContent = "证据来源发生变化，请重新查询。";
    }
    await Promise.all([
      loadImages(true),
      ["delete", "index"].includes(action) ? loadDocuments(true) : Promise.resolve()
    ]);
  } catch (error) {
    showNotice(error.message, "danger");
    setButtonBusy(button, false);
    if (action === "delete") await Promise.all([loadImages(true), loadDocuments(true)]);
  }
}

$("#imageList").onclick = event => {
  const button = event.target.closest("[data-image-action]");
  if (!button) return;
  imageAction(button.dataset.imageAction, Number(button.dataset.imageId), button);
};

$("#taskDetail").onclick = async event => {
  const button = event.target.closest("[data-image-action]");
  if (!button) return;
  await imageAction(button.dataset.imageAction, Number(button.dataset.imageId), button);
  if (currentView === "task" && currentTaskID) await openTask(currentTaskID);
};

async function loadDocuments(keepPolling = false) {
  clearTimeout(documentTimer);
  try {
    let documents = await request("/documents", {cache: "no-store"});
    documents = Array.isArray(documents) ? documents : [];
    const ready = documents.some(document => document.status === "ready");
    const active = documents.some(document => ["uploaded", "parsing", "embedding", "indexing"].includes(document.status));
    documentPollFailures = 0;
    ragReady = ready;
    $("#ragSubmit").disabled = !ready;
    $("#documentList").innerHTML = documents.length ? documents.map(document => `
      <article class="card"><div class="card-title"><strong>${escapeHTML(document.filename)}</strong><span class="status ${document.status}">${escapeHTML(document.status)}</span></div>
      <small>Document #${document.id} · Task #${document.indexing_task_id}</small>
      ${document.error_message ? `<p class="danger">${escapeHTML(document.error_message)}</p>` : ""}
      <div class="actions">${document.status === "failed" ? `<button onclick="retryTask(${document.indexing_task_id}, this);loadDocuments(true)">重试索引</button>` : ""}<button class="secondary" onclick="deleteDocument(${document.id}, this)">${document.status === "deleting" ? "重试删除" : "删除"}</button></div></article>`).join("") : `<div class="empty">还没有文档。上传 Markdown、TXT 或 JSON 后才能进行 RAG 查询。</div>`;
    localizeStatuses($("#documentList"));
    const ragResult = $("#ragResult");
    if (ragResult.classList.contains("empty")) {
      if (ready) {
        ragResult.textContent = "文档已就绪，请输入问题开始检索。";
      } else if (documents.length) {
        ragResult.textContent = "文档正在建立索引，完成后即可查询。";
      } else {
        ragResult.textContent = "上传文档并完成索引后即可查询。";
      }
    }
    if ((active || keepPolling) && currentView === "knowledge") documentTimer = setTimeout(() => loadDocuments(false), 1000);
  } catch (error) {
    showNotice(error.message, "danger");
    documentPollFailures += 1;
    if (retryable(error) && currentView === "knowledge") {
      documentTimer = setTimeout(() => loadDocuments(true), Math.min(1000 * (2 ** documentPollFailures), 10000));
    }
  }
}

async function deleteDocument(id, button) {
  if (!confirm("确认删除文档及其向量索引？")) return;
  ragRequestController?.abort();
  knowledgeVersion += 1;
  setButtonBusy(button, true, "正在删除…");
  try {
    await request(`/documents/${id}`, {method: "DELETE"});
    showNotice("文档已删除。", "success");
    $("#ragResult").className = "stack empty";
    $("#ragResult").textContent = "证据来源发生变化，请重新查询。";
    await loadDocuments();
  } catch (error) {
    showNotice(error.message, "danger");
    setButtonBusy(button, false);
    loadDocuments();
  }
}

$("#ragForm").onsubmit = async event => {
  event.preventDefault();
  const button = event.submitter;
  ragRequestController?.abort();
  ragRequestController = new AbortController();
  const requestVersion = ++ragRequestVersion;
  const sourceVersion = knowledgeVersion;
  setButtonBusy(button, true, "正在检索…");
  try {
    const data = await request("/rag/query", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({question: $("#question").value, top_k: 5}),
      signal: ragRequestController.signal
    }, 85000);
    if (requestVersion !== ragRequestVersion || sourceVersion !== knowledgeVersion || currentView !== "knowledge") return;
    $("#ragResult").className = "stack";
    const citations = data.citations || [];
    const usedSources = new Set([...String(data.answer || "").matchAll(/\[S(\d+)\]/g)].map(match => `S${match[1]}`));
    const used = citations.filter(citation => usedSources.has(String(citation.source)));
    const candidates = citations.filter(citation => !usedSources.has(String(citation.source)));
    const citationCard = citation => `<article class="card citation"><div class="card-title"><strong>[${escapeHTML(citation.source)}] ${escapeHTML(citation.title)}</strong><span class="citation-score">${Number(citation.score).toFixed(3)}</span></div><p>${escapeHTML(citation.summary)}</p><small>文档 #${citation.document_id} · 行 ${citation.start_line}–${citation.end_line} · Chunk ${escapeHTML(citation.chunk_id)}</small></article>`;
    $("#ragResult").innerHTML = `<article class="card report"><h3>证据回答</h3><div class="markdown">${renderMarkdown(data.answer)}</div></article>
      ${used.length ? `<section><h3>回答实际引用</h3><div class="cards">${used.map(citationCard).join("")}</div></section>` : `<div class="card warning">回答没有标注可核验引用，请调整问题或检查模型输出。</div>`}
      ${candidates.length ? `<details class="card retrieval-candidates"><summary>其他检索候选（${candidates.length}）</summary><p class="muted">这些片段参与了召回，但没有被当前回答直接引用。</p><div class="cards">${candidates.map(citationCard).join("")}</div></details>` : ""}`;
  } catch (error) {
    if (error.code === "REQUEST_CANCELLED") return;
    $("#ragResult").innerHTML = `<div class="card danger">${escapeHTML(error.message)}</div>`;
  } finally {
    if (requestVersion === ragRequestVersion) {
      setButtonBusy(button, false);
      button.disabled = !ragReady;
    }
  }
};

async function loadSkills() {
  const runButton = $("#skillForm button[type=submit]");
  runButton.disabled = true;
  try {
    const skills = await request("/skills");
    $("#skillName").replaceChildren();
    skills.forEach(skill => $("#skillName").add(new Option(skill.description, skill.name)));
    updateSkillInput();
    runButton.disabled = false;
  } catch (error) {
    $("#skillResult").innerHTML = `<div class="card danger">${escapeHTML(error.message)}<div class="actions"><button type="button" onclick="loadSkills()">重新加载 Skill</button></div></div>`;
  }
}
$("#skillName").onchange = updateSkillInput;

health();
loadRecords();
loadSkills();
setInterval(health, 15000);
