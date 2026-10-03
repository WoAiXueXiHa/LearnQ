let selectedAuthorityClaim = null;
let authorityPanelRevision = 0;

function resetAuthorityPanel() {
  authorityPanelRevision++;
  selectedAuthorityClaim = null;
  $("#authorityPanel").innerHTML = "";
}

async function refreshAuthoritySources() {
  try {
    const rows = await request("/authority-sources");
    $("#authoritySources").innerHTML = rows.map(row => `<article class="card"><h4>${escapeHTML(row.topic)}</h4><p>${escapeHTML(row.url)}</p><button type="button" data-capture-source="${row.id}">保存当前文档快照</button><button type="button" data-list-snapshots="${row.id}">查看快照</button></article>`).join("") || "<p>尚未登记来源。</p>";
  } catch (error) { practiceError(error); }
}

async function showAuthorityClaim(id) {
  const ticket = ++authorityPanelRevision;
  selectedAuthorityClaim = id;
  const data = await request(`/authority-claims/${id}`);
  if (ticket !== authorityPanelRevision) return;
  $("#authorityPanel").innerHTML = `<article class="card"><h3>待核查疑点</h3><p>${escapeHTML(data.claim.assertion)}</p><details><summary>文章原文依据</summary>${data.article_evidence.map(chunk => `<p>${escapeHTML(chunk.content)}</p><a href="/api/v1/documents/${data.document_id}/indexes/${data.index_id}/chunks/${chunk.id}" target="_blank" rel="noopener">固定版本原文</a>`).join("")}${data.image_ref_ids.map(ref => `<a href="/api/v1/documents/${data.document_id}/indexes/${data.index_id}/images/${ref}" target="_blank" rel="noopener">图片证据</a>`).join(" ")}</details>${data.checks.map(result => `<details open><summary>外部依据 · ${escapeHTML(result.check.status)}${result.expired ? " · 已过期" : !result.evidence_valid ? " · 依据待重新验证" : ""}</summary><p>${escapeHTML(result.snapshot.title)} · ${escapeHTML(result.snapshot.version_label)}</p><p>${escapeHTML(result.check.excerpt)}</p><a href="/api/v1/authority-snapshots/${result.snapshot.id}" target="_blank" rel="noopener">固定来源快照</a><h4>系统判断</h4><p>${escapeHTML(result.check.judgment)}：${escapeHTML(result.check.explanation)}</p><p>核查模型：${escapeHTML(result.check.model)}</p>${(result.reviews || []).map(review => `<p>用户意见：${escapeHTML(review.disposition)} · ${escapeHTML(review.comment)}</p>`).join("")}${result.check.status === "ready" ? `<button type="button" data-authority-review="${result.check.id}" data-disposition="confirmed" ${!result.evidence_valid || result.check.judgment === "uncertain" ? "disabled" : ""}>确认依据与判断</button><button type="button" data-authority-review="${result.check.id}" data-disposition="disputed">质疑判断</button><button type="button" data-authority-review="${result.check.id}" data-disposition="uncertain">仍无法确定</button>` : ""}${result.check.status === "failed" ? `<button type="button" data-authority-retry="${result.check.task_id}">重试核查</button>` : ""}</details>`).join("")}<p>选择下方登记来源的快照，再提交与疑点相关的摘录。原反馈保持不变。</p><button type="button" data-show-claim="${id}">刷新核查结果</button></article>`;
}

$("#authorityRegister").onsubmit = async event => {
  event.preventDefault(); event.stopPropagation();
  const formElement = event.currentTarget;
  if (formElement.dataset.saving) return;
  formElement.dataset.saving = "true";
  setButtonBusy(event.submitter, true);
  const form = new FormData(formElement);
  try { await request("/authority-sources", practiceJSON("POST", {topic: form.get("topic"), url: form.get("url")})); await refreshAuthoritySources(); }
  catch (error) { practiceError(error); }
  finally { delete formElement.dataset.saving; setButtonBusy(event.submitter, false); }
};

$("#practice").addEventListener("click", async event => {
  const button = event.target.closest("button");
  if (!button || !button.matches("[data-authority-review],[data-authority-retry],[data-capture-source],[data-list-snapshots],[data-use-snapshot],[data-show-claim],[data-feedback-claims]")) return;
  setButtonBusy(button, true);
  try {
    if (button.dataset.authorityReview) {
      const ticket = authorityPanelRevision;
      const claimId = selectedAuthorityClaim;
      const comment = prompt("说明你的确认依据、质疑原因或尚未确定的条件；原判断会保留。");
      if (!comment || !comment.trim()) return;
      await request(`/authority-checks/${button.dataset.authorityReview}/reviews`, practiceJSON("POST", {disposition: button.dataset.disposition, comment}));
      if (ticket === authorityPanelRevision && claimId === selectedAuthorityClaim && claimId) await showAuthorityClaim(claimId);
    } else if (button.dataset.authorityRetry) {
      const ticket = authorityPanelRevision;
      const claimId = selectedAuthorityClaim;
      await request(`/tasks/${button.dataset.authorityRetry}/retry`, {method: "POST"});
      if (ticket === authorityPanelRevision && claimId === selectedAuthorityClaim && claimId) await showAuthorityClaim(claimId);
    } else if (button.dataset.captureSource) {
      const version = prompt("填写文档版本或明确的适用版本；未知可留空，届时只能保持待核查。");
      if (version === null) return;
      await request(`/authority-sources/${button.dataset.captureSource}/snapshots`, practiceJSON("POST", {version_label: version}), 30000);
      showNotice("来源快照已保存。");
    } else if (button.dataset.listSnapshots) {
      const ticket = authorityPanelRevision;
      const rows = await request(`/authority-sources/${button.dataset.listSnapshots}/snapshots`);
      if (ticket !== authorityPanelRevision) return;
      $("#authorityPanel").insertAdjacentHTML("beforeend", `<article class="card"><h3>来源快照</h3>${rows.map(row => `<p>${escapeHTML(row.title)} · ${escapeHTML(row.version_label || "版本未知")} <button type="button" data-use-snapshot="${row.id}">阅读并选取摘录</button></p>`).join("") || "<p>尚无快照。</p>"}</article>`);
    } else if (button.dataset.useSnapshot) {
      const ticket = authorityPanelRevision;
      const claimId = selectedAuthorityClaim;
      const result = await request(`/authority-snapshots/${button.dataset.useSnapshot}`);
      if (ticket !== authorityPanelRevision || claimId !== selectedAuthorityClaim) return;
      $$("#authorityCheckForm").forEach(form => form.closest(".card").remove());
      $("#authorityPanel").insertAdjacentHTML("beforeend", `<article class="card"><h3>${escapeHTML(result.snapshot.title)}</h3><details><summary>固定来源全文</summary><pre style="white-space:pre-wrap">${escapeHTML(result.snapshot.extracted_text)}</pre></details><form id="authorityCheckForm"><input name="snapshot_id" type="hidden" value="${result.snapshot.id}"><label>相关原文摘录<textarea name="excerpt" required maxlength="16000"></textarea></label><label>版本、条件与上下文说明<textarea name="context_note" required maxlength="8000"></textarea></label><button type="submit" ${result.expired || !result.version_known || !claimId ? "disabled" : ""}>提交断言核查</button></form></article>`);
      $("#authorityCheckForm").onsubmit = async submitEvent => {
        submitEvent.preventDefault();
        if (ticket !== authorityPanelRevision || claimId !== selectedAuthorityClaim) return;
        const formElement = submitEvent.currentTarget;
        if (formElement.dataset.saving) return;
        formElement.dataset.saving = "true";
        setButtonBusy(submitEvent.submitter, true);
        const form = new FormData(formElement);
        try { await request(`/authority-claims/${claimId}/checks`, practiceJSON("POST", {snapshot_id: Number(form.get("snapshot_id")), excerpt: form.get("excerpt"), context_note: form.get("context_note")})); if (ticket === authorityPanelRevision) await showAuthorityClaim(claimId); }
        catch (error) { practiceError(error); }
        finally { delete formElement.dataset.saving; setButtonBusy(submitEvent.submitter, false); }
      };
    } else if (button.dataset.showClaim) await showAuthorityClaim(Number(button.dataset.showClaim));
    else if (button.dataset.feedbackClaims) {
      const ticket = ++authorityPanelRevision;
      selectedAuthorityClaim = null;
      const rows = await request(`/answer-feedback/${button.dataset.feedbackClaims}/claims`);
      if (ticket !== authorityPanelRevision) return;
      if (!rows.length) { showNotice("这条反馈没有待核查冲突。"); return; }
      $("#authorityPanel").innerHTML = rows.map(row => `<article class="card"><p>${escapeHTML(row.assertion)}</p><button type="button" data-show-claim="${row.id}">展开双来源核查</button></article>`).join("");
    }
  } catch (error) { practiceError(error); }
  finally { setButtonBusy(button, false); }
});

refreshAuthoritySources();
