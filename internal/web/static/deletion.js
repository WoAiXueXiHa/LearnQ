/* Confirmed deletion is persisted and continued by the worker. */
(() => {
  let timer = null;
  let revision = 0;
  const panel = $("#practiceDeletion");
  const labels = {question_sets: "题组", practice_questions: "题目", question_set_edits: "题组编辑记录", practice_attempts: "作答", answer_feedback: "逐题反馈", feedback_corrections: "纠错记录", practice_reviews: "复习安排", authority_claims: "待核查断言", authority_checks: "核查记录", authority_check_reviews: "核查确认记录", authority_snapshots: "来源快照", article_images: "文章图片引用", image_evidence: "图片描述证据", document_chunks: "文字证据片段", document_indexes: "文章版本"};
  const states = {quiescing: "等待正在执行的任务停止", vectors_pending: "清理检索数据与文章记录", images_pending: "清理无其他引用的图片", complete: "清理完成"};
  async function progress(id, ticket) {
    if (ticket !== revision) return;
    try {
      const job = await request(`/documents/${id}/deletion`);
      if (ticket !== revision) return;
      panel.innerHTML = `<article class="card"><h3>文章 ${id}：${escapeHTML(states[job.status] || job.status)}</h3><p>关闭页面不会停止清理；遇到故障后台会继续重试。</p>${job.last_error ? `<p role="alert">${escapeHTML(job.last_error)}</p>` : ""}<p>其他文章或记录仍引用的图片保留。已有备份按保留期处理，不会立即擦除。</p></article>`;
      if (job.status === "complete") {
        if (practiceState.document && practiceState.document.id === id) {
          clearTimeout(practiceState.timer);
          practiceState.revision++;
          resetAuthorityPanel();
          practiceState.document = practiceState.set = practiceState.attempt = null;
          for (const selector of ["#practiceImages", "#practiceSet", "#practiceAttempt"]) $(selector).innerHTML = "";
        }
        await refreshPractice();
      } else timer = setTimeout(() => progress(id, ticket), 5000);
    } catch (error) {
      if (ticket !== revision) return;
      panel.textContent = `暂时无法读取清理进度：${error.message}。正在重试。`;
      timer = setTimeout(() => progress(id, ticket), 5000);
    }
  }
  $("#practiceArticles").addEventListener("click", async event => {
    const button = event.target.closest("[data-deletion-document]");
    if (!button) return;
    const id = Number(button.dataset.deletionDocument);
    clearTimeout(timer);
    const ticket = ++revision;
    setButtonBusy(button, true);
    try {
      const documents = await request("/documents");
      if (ticket !== revision) return;
      if (documents.find(d => d.id === id)?.status === "deleting") { await progress(id, ticket); return; }
      const result = await request(`/documents/${id}/deletion-plan`);
      if (ticket !== revision) return;
      const counts = Object.entries(result.plan.counts).map(([name, count]) => `<li>${escapeHTML(labels[name] || name)}：${count}</li>`).join("");
      panel.innerHTML = `<form class="card"><h3>彻底删除文章 ${id}</h3><p>将删除原文、历史版本及以下练习数据，此操作无法通过恢复归档撤销。请先导出需要保留的记录。</p><ul>${counts}</ul><p>共享图片保留；已有备份不会立即擦除，需按备份保留期处理。</p><label>输入 ${escapeHTML(result.required_confirmation)} 确认<input name="confirmation" required autocomplete="off"></label><button type="submit">确认彻底删除</button><button type="button" data-deletion-cancel>取消</button></form>`;
      panel.querySelector("[data-deletion-cancel]").onclick = () => { revision++; panel.innerHTML = ""; };
      panel.querySelector("form").onsubmit = async submit => {
        submit.preventDefault();
        if (ticket !== revision) return;
        const confirmation = new FormData(submit.target).get("confirmation");
        if (confirmation !== result.required_confirmation) { showNotice("请输入完整的确认文字。", "danger"); return; }
        const confirm = submit.target.querySelector('[type="submit"]');
        setButtonBusy(confirm, true);
        try {
          await request(`/documents/${id}/purge`, practiceJSON("POST", {plan_hash: result.plan_hash, confirmation}), 60000);
          await progress(id, ticket);
        } catch (error) { practiceError(error); }
        finally { setButtonBusy(confirm, false); }
      };
    } catch (error) { practiceError(error); }
    finally { setButtonBusy(button, false); }
  });
})();
