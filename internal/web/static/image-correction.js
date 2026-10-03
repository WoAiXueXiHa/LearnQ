let imageCorrectionRevision = 0;
$("#practiceImages").addEventListener("click", async event => {
  const button = event.target.closest("[data-correct-image]");
  if (!button || !practiceState.document) return;
  const ticket = ++imageCorrectionRevision;
  const document = practiceState.document;
  const current = () => ticket === imageCorrectionRevision && practiceState.document?.id === document.id && practiceState.document?.active_index_id === document.active_index_id;
  const refId = Number(button.dataset.correctImage);
  try {
    const evidence = await request(`/documents/${document.id}/indexes/${document.active_index_id}/images/${refId}`);
    if (!current()) return;
    const description = JSON.parse(evidence.effective_description || "{}");
    $("#imageCorrectionCard")?.remove();
    const fields = [["title", "标题"], ["summary", "图中可见内容摘要"], ["extracted_text", "图中文字"], ["key_points", "关键节点、关系与箭头（每行一项）"], ["learning_explanation", "解释与推断"], ["uncertainties", "仍不确定的内容（每行一项）"]];
    $("#practiceImages").insertAdjacentHTML("beforeend", `<article class="card" id="imageCorrectionCard"><h3>修订图片描述</h3><a href="${escapeHTML(evidence.content_url)}" target="_blank" rel="noopener">对照原图</a><details><summary>原始视觉输出</summary><p>${escapeHTML(JSON.stringify(evidence.description, null, 2))}</p></details>${(evidence.corrections || []).map(c => `<p>历史修订：${escapeHTML(c.comment)} · ${escapeHTML(c.created_at)}</p>`).join("")}<p>修订记录会保留。重建文章后，新题组才使用修订；历史题组与作答不变。</p><form id="imageCorrectionForm">${fields.map(([key, label]) => `<label>${label}<textarea name="${key}" maxlength="12000" ${key === "summary" ? "required" : ""}>${escapeHTML(Array.isArray(description[key]) ? description[key].join("\n") : description[key] || "")}</textarea></label>`).join("")}<label>修订依据<textarea name="comment" required maxlength="8000"></textarea></label><button type="submit">保存修订记录</button><button type="button" data-cancel-correction>取消</button></form></article>`);
    const card = $("#imageCorrectionCard");
    card.querySelector("[data-cancel-correction]").onclick = () => { imageCorrectionRevision++; card.remove(); };
    $("#imageCorrectionForm").onsubmit = async submit => {
      submit.preventDefault();
      if (!current() || !card.isConnected || submit.currentTarget.dataset.saving) return;
      const formElement = submit.currentTarget;
      formElement.dataset.saving = "true";
      const form = new FormData(formElement);
      const corrected = Object.fromEntries(fields.map(([key]) => [key, ["key_points", "uncertainties"].includes(key) ? String(form.get(key)).split("\n").map(s => s.trim()).filter(Boolean) : form.get(key)]));
      setButtonBusy(submit.submitter, true);
      try {
        await request(`/documents/${document.id}/image-corrections`, practiceJSON("POST", {image_ref_id: refId, description: corrected, comment: form.get("comment")}));
        if (!current() || !card.isConnected) return;
        card.innerHTML = '<p>修订已保存。重新下载、索引可能触发模型调用，仍受预算限制。</p><button type="button" id="rebuildCorrectedArticle">重建文章，后续出题使用修订</button>';
        $("#rebuildCorrectedArticle").onclick = async rebuild => {
          const rebuildButton = rebuild.currentTarget;
          setButtonBusy(rebuildButton, true);
          try { await request(`/documents/${document.id}/reindex`, {method: "POST"}); showNotice("重建已排队，完成后刷新并选择文章。"); }
          catch (error) { practiceError(error); }
          finally { setButtonBusy(rebuildButton, false); }
        };
      } catch (error) { practiceError(error); }
      finally { delete formElement.dataset.saving; setButtonBusy(submit.submitter, false); }
    };
  } catch (error) { practiceError(error); }
});
