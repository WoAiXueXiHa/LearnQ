const moneyCNY = value => value == null ? "未知" : `¥${(Number(value) / 1000000).toFixed(6)}`;

let modelDiagnosticsRevision = 0;
async function refreshModelDiagnostics() {
  const ticket = ++modelDiagnosticsRevision;
  try {
    const [budget, prices, ledger] = await Promise.all([request("/model-budget"), request("/model-prices"), request("/model-calls")]);
    if (ticket !== modelDiagnosticsRevision) return;
    $("#modelBudget").innerHTML = `<h3>${escapeHTML(budget.mode || "未知模式")} · ${escapeHTML(budget.day)} UTC</h3><p>每日额度 ${moneyCNY(budget.daily_budget_microcny)} · 单次预留 ${moneyCNY(budget.call_reserve_microcny)} · 今日已预留 ${moneyCNY(budget.reserved_microcny)}</p><p>预留额用于限制新调用，不代表实际扣费。失败和超时的预留不会自动退回。预算为零会阻止真实调用。</p><p>预算在本机 .env 中配置，重启 API 与 worker 后生效；真实调用还需要匹配的价格版本。</p>`;
    $("#modelPrices").innerHTML = prices.map(p => `<article class="card"><h4>${escapeHTML(p.model)}</h4><p>${escapeHTML(p.version_label)} · 每百万输入 ${moneyCNY(p.input_microcny_per_million)} / 输出 ${moneyCNY(p.output_microcny_per_million)}</p><p>输入上限 ${p.max_input_tokens} · 输出上限 ${p.max_output_tokens}</p></article>`).join("") || "<p>尚未登记价格；真实调用将被拦截。</p>";
    $("#modelCalls").innerHTML = ledger.calls.map(call => `<article class="card"><h4>调用 ${call.id} · ${escapeHTML(call.kind)} · ${escapeHTML(call.status)}</h4><p>${escapeHTML(call.mode)} · ${escapeHTML(call.model)} · ${call.duration_ms} ms</p><p>${call.usage_known ? `输入 ${call.input_tokens} / 输出 ${call.output_tokens} token` : "供应商用量未知"} · 估算 ${moneyCNY(call.estimated_microcny)} · 预留 ${moneyCNY(call.reserved_microcny)}</p><p>${escapeHTML(call.error_code || "")}</p>${call.mode === "real" ? `<button type="button" data-model-billing="${call.id}">追加账单核对记录</button>` : ""}${(ledger.billings || []).filter(b => b.model_call_id === call.id).map(b => `<p>账单核对：${moneyCNY(b.billed_microcny)} · ${escapeHTML(b.note)}</p>`).join("")}</article>`).join("") || "<p>尚无调用记录。</p>";
  } catch (error) { practiceError(error); }
}

$("#tab-diagnostics").onclick = () => { switchView("diagnostics"); refreshModelDiagnostics(); };
$("#diagnosticsRefresh").onclick = refreshModelDiagnostics;
$("#modelPriceForm").onsubmit = async event => {
  event.preventDefault();
  const formElement = event.currentTarget;
  if (formElement.dataset.saving) return;
  formElement.dataset.saving = "true";
  const form = new FormData(formElement);
  const row = {model: form.get("model"), version_label: form.get("version_label"), input_microcny_per_million: Math.round(Number(form.get("input_price")) * 1000000), output_microcny_per_million: Math.round(Number(form.get("output_price")) * 1000000), max_input_tokens: Number(form.get("max_input_tokens")), max_output_tokens: Number(form.get("max_output_tokens"))};
  setButtonBusy(event.submitter, true);
  try { await request("/model-prices", practiceJSON("POST", row)); showNotice("价格版本已保存。"); await refreshModelDiagnostics(); }
  catch (error) { practiceError(error); }
  finally { delete formElement.dataset.saving; setButtonBusy(event.submitter, false); }
};
$("#modelCalls").addEventListener("click", async event => {
  const button = event.target.closest("[data-model-billing]");
  if (!button) return;
  const amount = prompt("填写供应商账单中该次调用的人民币金额；无法逐次对应时不要填写。");
  if (amount === null || !amount.trim()) return;
  const micro = Math.round(Number(amount) * 1000000);
  if (!Number.isSafeInteger(micro) || micro < 0) { showNotice("请输入有效金额。", "danger"); return; }
  const note = prompt("填写账单依据和对应关系，不要填写密钥。");
  if (!note || !note.trim()) return;
  setButtonBusy(button, true);
  try { await request(`/model-calls/${button.dataset.modelBilling}/billings`, practiceJSON("POST", {billed_microcny: micro, note})); await refreshModelDiagnostics(); }
  catch (error) { practiceError(error); }
  finally { setButtonBusy(button, false); }
});
