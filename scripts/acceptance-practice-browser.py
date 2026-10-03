#!/usr/bin/env python3
"""DOM-driven Fake workflow. Requires Playwright and an isolated running API."""
import argparse
import json
from pathlib import Path
import re
import zipfile
from urllib.parse import urlparse


def run(args):
    from playwright.sync_api import expect, sync_playwright
    args.evidence_dir.mkdir(parents=True,exist_ok=False)
    checks=[]
    errors=[]
    result={"status":"failed","quality_validated":False,"checks":checks}
    with sync_playwright() as playwright:
        browser=playwright.chromium.launch()
        context=browser.new_context(viewport={"width":1280,"height":900})
        context.tracing.start(snapshots=True,screenshots=False,sources=True)
        page=context.new_page()
        page.on("pageerror",lambda error:errors.append(str(error)))
        # Unexpected alerts are failures, including accidental HTML/script execution.
        def handle_dialog(dialog):
            if dialog.type != "prompt": errors.append("unexpected browser dialog: "+dialog.type)
            dialog.accept("合成验收意见：仅检查流程，不证明模型质量" if dialog.type=="prompt" else None)
        page.on("dialog",handle_dialog)

        def captured_click(selector,path):
            with page.expect_response(lambda response: response.request.method=="POST" and urlparse(response.url).path=="/api/v1"+path) as response:
                page.locator(selector).click()
            return response.value

        def snapshot(name):
            (args.evidence_dir/(name+".html")).write_text(page.content())
            page.screenshot(path=str(args.evidence_dir/(name+".png")), full_page=True)

        try:
            budget=context.request.get(args.base_url+"/api/v1/model-budget")
            assert budget.ok and budget.json()["data"]["mode"]=="fake", "browser acceptance must use Fake API"
            page.goto(args.base_url)
            expect(page.locator("#practice")).to_be_visible()
            expect(page.locator("#diagnostics")).to_be_hidden()
            checks.append("practice is the default product view")
            # Registration persists metadata only: never capture or fetch a live
            # official page as part of this isolated Fake browser workflow.
            register = page.locator("#authorityRegister")
            register.locator("xpath=ancestor::details").evaluate("element => element.open = true")
            register.locator('[name="topic"]').fill("browser-<img src=x onerror=alert(1)>")
            register.locator('[name="url"]').fill("http://127.0.0.1/private")
            rejected = captured_click('#authorityRegister [type="submit"]', "/authority-sources")
            assert rejected.status == 422
            register.locator('[name="url"]').fill("https://go.dev/doc/effective_go")
            registered = captured_click('#authorityRegister [type="submit"]', "/authority-sources")
            assert registered.status == 201
            source_id = registered.json()["data"]["id"]
            source_card = page.locator(f'[data-list-snapshots="{source_id}"]').locator("xpath=ancestor::article")
            expect(source_card.locator("h4")).to_have_text("browser-<img src=x onerror=alert(1)>")
            expect(source_card.locator("img")).to_have_count(0)
            source_card.locator(f'[data-list-snapshots="{source_id}"]').click()
            expect(page.locator("#authorityPanel")).to_contain_text("尚无快照")
            checks.append("authority registration rejects non-HTTPS URL, escapes topic and lists empty snapshots without fetching")
            snapshot("authority-registered")
            source="# 浏览器合成文章\n\n事务将操作组成整体。\n\n失败时回滚，成功时提交。\n\n并发需要检查状态。\n\n重试保留业务标识并去重。\n\n外部副作用需要独立恢复。\n\n|操作|结果|\n|---|---|\n|失败|回滚|\n\n```go\n// 中文代码\nrollback()\n```\n\n<script>alert(1)</script>\n\n[危险](javascript:alert%281%29)\n"
            page.locator("#practiceFile").set_input_files({"name":"browser-<img src=x onerror=alert(1)>.md","mimeType":"text/markdown","buffer":source.encode()})
            upload=captured_click('#practiceUpload [type="submit"]',"/documents")
            assert upload.ok
            document_id=upload.json()["data"]["document_id"]
            for _ in range(120):
                document=context.request.get(f"{args.base_url}/api/v1/documents/{document_id}/status").json()["data"]
                if document["status"]=="ready":break
                assert document["status"]!="failed", "browser article indexing failed"
                page.wait_for_timeout(1000)
            else:raise AssertionError("browser article indexing timed out")
            page.locator("#practiceRefresh").click()
            page.locator(f'[data-document="{document_id}"]').click()
            expect(page.locator("#authorityPanel")).to_be_empty()
            checks.append("selecting an article clears the previous authority panel")
            generated=captured_click("#practiceGenerate",f"/documents/{document_id}/question-sets")
            assert generated.ok
            set_id=generated.json()["data"]["id"]
            expect(page.locator("#practiceSet h3")).to_contain_text("待编辑确认",timeout=120000)
            expect(page.locator("[data-question]")).to_have_count(5)
            first=page.locator('[data-question="0"]')
            first.fill(first.input_value()+"（浏览器编辑）")
            page.locator("#practiceSaveSet").click()
            expect(page.locator('[data-question="0"]')).to_have_value(re.compile("浏览器编辑"))
            confirmed=captured_click("#practiceConfirm",f"/question-sets/{set_id}/confirm")
            assert confirmed.ok
            expect(page.locator("#practiceStart")).to_be_visible()
            snapshot("questions-confirmed")
            attempt=captured_click("#practiceStart",f"/question-sets/{set_id}/attempts")
            assert attempt.ok
            attempt_id=attempt.json()["data"]["id"]
            expect(page.locator("[data-answer]")).to_have_count(5)
            for width in [390,768,1440]:
                page.set_viewport_size({"width":width,"height":900})
                assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth + 1"), f"practice overflows at {width}px"
                boxes=page.locator('[data-answer]').evaluate_all("nodes=>nodes.map(n=>({width:n.getBoundingClientRect().width,parent:n.parentElement.getBoundingClientRect().width,display:getComputedStyle(n.parentElement).display}))")
                assert all(b["display"]=="grid" and b["width"]>b["parent"]*.95 for b in boxes), boxes
                snapshot(f"practice-answers-{width}")
            page.set_viewport_size({"width":1280,"height":900})
            checks.append("five answer fields use vertical full-width layout at 390/768/1440px without page overflow")
            page.locator("#practiceRefresh").click()
            for width in [390,768,1440]:
                page.set_viewport_size({"width":width,"height":900})
                for selector,panel in [(f'[data-open-set="{set_id}"]',"#practiceSet"),(f'[data-open-attempt="{attempt_id}"]',"#practiceAttempt")]:
                    button=page.locator("#practiceHistory "+selector)
                    expect(button).to_be_visible()
                    button.scroll_into_view_if_needed()
                    button.click()
                    heading=page.locator(panel+" h3").first
                    expect(heading).to_be_focused()
                    assert 0 <= heading.bounding_box()["y"] < 900
            page.set_viewport_size({"width":1280,"height":900})
            checks.append("history buttons scroll to content and focus its heading at 390/768/1440px")
            before=context.request.get(f"{args.base_url}/api/v1/practice-attempts/{attempt_id}").json()["data"]
            assert all(key not in before for key in ["reference_points","reference_items","reference_evidence","reference_metadata","feedback"])
            expect(page.locator("#practiceAttempt")).not_to_contain_text("参考要点")
            checks.append("five questions edited/confirmed and private answers hidden")
            for number in range(5):page.locator(f'[data-answer="{number}"]').fill(f"第{number+1}题草稿：事务与恢复。")
            # Wait for persistence, not merely a button click or optimistic UI.
            with page.expect_response(lambda response: response.request.method=="PUT" and urlparse(response.url).path==f"/api/v1/practice-attempts/{attempt_id}/answers") as saved:
                page.locator("#practiceSaveAnswers").click()
            assert saved.value.ok
            page.reload()
            page.locator(f'[data-open-attempt="{attempt_id}"]').click()
            expect(page.locator('[data-answer="0"]')).to_have_value("第1题草稿：事务与恢复。")
            checks.append("saved answer draft survives browser reload")
            submitted=captured_click("#practiceSubmit",f"/practice-attempts/{attempt_id}/submit")
            assert submitted.ok
            expect(page.locator("#practiceAttempt h3").first).to_contain_text("反馈已生成",timeout=120000)
            expect(page.locator('[data-answer="0"]')).to_have_attribute("readonly","")
            assert page.locator("#practiceAttempt .reference-section").count()==5
            snapshot("feedback-ready")
            checks.append("submission freezes answers and reveals five feedback/reference sections")
            submitted_data=context.request.get(f"{args.base_url}/api/v1/practice-attempts/{attempt_id}").json()["data"]
            assert all(2<=len(items)<=4 for items in submitted_data["reference_items"])
            assert page.locator('[data-feedback-claims]').count()==0, "no conflict should not show check button"
            section=page.locator("#practiceAttempt .reference-section").first
            section.evaluate("element=>element.open=true")
            evidence_button=section.locator('[data-evidence]').first
            evidence_button.click()
            expect(page.locator("#evidencePanel")).to_be_visible()
            expect(page.locator("#evidenceBody")).to_contain_text("文章原文")
            expect(page.locator("#evidenceBody .markdown")).to_contain_text("事务")
            original_index=before["index_id"]
            assert all(meta["index_id"]==original_index for meta in submitted_data["reference_metadata"].values())
            expect(page.locator("#evidenceBody script,#evidenceBody img")).to_have_count(0)
            for width in [390,768,1440]:
                page.set_viewport_size({"width":width,"height":900})
                assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth + 1"), f"feedback/evidence overflows {width}px"
                snapshot(f"practice-evidence-{width}")
            page.locator('[data-evidence-mode="source"]').click()
            expect(page.locator("#evidenceBody .source-line.selected").first).to_be_visible()
            expect(page.locator("#evidenceBody")).to_contain_text("中文代码")
            page.keyboard.press("Escape")
            expect(page.locator("#evidencePanel")).not_to_be_visible()
            expect(evidence_button).to_be_focused()
            evidence_path=evidence_button.get_attribute("data-evidence")
            def invalid_evidence(route):
                route.fulfill(status=410,content_type="application/json",body=json.dumps({"error":{"message":"合成校验失败","code":"EVIDENCE_INVALID"}}))
            page.route(args.base_url+"/api/v1"+evidence_path,invalid_evidence)
            evidence_button.click()
            expect(page.locator("#evidenceBody")).to_contain_text("固定版本依据无法读取")
            expect(page.locator("#evidenceBody .markdown")).to_have_count(0)
            page.keyboard.press("Escape")
            page.unroute(args.base_url+"/api/v1"+evidence_path,invalid_evidence)
            expect(page.locator('[data-answer="0"]')).to_have_value("第1题草稿：事务与恢复。")
            page.set_viewport_size({"width":1280,"height":900})
            checks.append("structured references open fixed safe evidence, numbered source, responsive panel and Escape restores focus without losing answers")
            page.locator('[data-correction][data-disposition="disputed"]').first.click()
            expect(page.locator("#practiceAttempt")).to_contain_text("历史意见")
            blocked=captured_click("#practiceSchedule",f"/practice-attempts/{attempt_id}/review")
            assert blocked.status==409
            page.locator('[data-correction][data-disposition="accepted"]').first.locator("xpath=ancestor::details").evaluate("element => element.open = true")
            page.locator('[data-correction][data-disposition="accepted"]').first.click()
            expect(page.locator("#practiceAttempt")).to_contain_text("已核对接受")
            scheduled=captured_click("#practiceSchedule",f"/practice-attempts/{attempt_id}/review")
            assert scheduled.ok
            review_id=scheduled.json()["data"]["id"]
            repeated=captured_click(f'[data-review="{review_id}"]',f"/practice-reviews/{review_id}/start")
            assert repeated.ok and repeated.json()["data"]["id"]!=attempt_id
            expect(page.locator('[data-answer="0"]')).to_have_value("")
            checks.append("correction history affects scheduling; review has independent blank answers")
            review_attempt_id = repeated.json()["data"]["id"]
            answers_url = f"{args.base_url}/api/v1/practice-attempts/{review_attempt_id}/answers"
            page.locator('[data-answer="0"]').fill("保存发起时的答案")

            def edit_before_save_returns(route):
                response = route.fetch()
                page.locator('[data-answer="0"]').fill("保存等待期间继续输入的答案")
                route.fulfill(response=response)

            page.route(answers_url, edit_before_save_returns)
            with page.expect_response(lambda response: response.url == answers_url and response.request.method == "PUT") as slow_saved:
                page.locator("#practiceSaveAnswers").click()
            assert slow_saved.value.ok
            expect(page.locator('[data-answer="0"]')).to_have_value("保存等待期间继续输入的答案")
            # Await the follow-up read/render before starting the next scenario.
            expect(page.locator("#practiceSaveAnswers")).to_be_enabled()
            expect(page.locator("#notice")).to_contain_text("尚未保存")
            persisted = context.request.get(f"{args.base_url}/api/v1/practice-attempts/{review_attempt_id}").json()["data"]
            assert persisted["answers"][0] == "保存发起时的答案"
            page.unroute(answers_url, edit_before_save_returns)
            checks.append("edits made while save response is pending survive refresh and remain explicitly unsaved")

            def switch_before_save_returns(route):
                response = route.fetch()
                page.locator("#tab-diagnostics").click()
                route.fulfill(response=response)

            page.route(answers_url, switch_before_save_returns)
            with page.expect_response(lambda response: response.url == answers_url and response.request.method == "PUT") as switched_save:
                page.locator("#practiceSaveAnswers").click()
            assert switched_save.value.ok
            expect(page.locator("#diagnostics")).to_be_visible()
            expect(page.locator("#practice")).to_be_hidden()
            page.unroute(answers_url, switch_before_save_returns)
            checks.append("save completion does not reopen practice after switching to diagnostics")
            page.locator("#tab-diagnostics").click()
            expect(page.locator("#diagnostics")).to_be_visible()
            expect(page.locator("#modelBudget")).to_contain_text("fake")
            checks.append("diagnostics are separate and display runtime budget")
            page.set_viewport_size({"width":390,"height":844})
            assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth + 1"), "diagnostics overflows narrow viewport"
            checks.append("diagnostics fit narrow viewport without document-wide horizontal overflow")
            snapshot("diagnostics")
            page.set_viewport_size({"width":1280,"height":900})
            page.locator("#tab-practice").click()
            archived=captured_click(f'[data-archive-document="{document_id}"]',f"/documents/{document_id}/archive")
            assert archived.ok
            expect(page.locator(f'[data-restore-document="{document_id}"]')).to_be_visible()
            page.locator(f'[data-open-attempt="{attempt_id}"]').click()
            expect(page.locator('[data-answer="0"]')).to_have_value("第1题草稿：事务与恢复。")
            restored=captured_click(f'[data-restore-document="{document_id}"]',f"/documents/{document_id}/restore")
            assert restored.ok
            with page.expect_download() as download:
                page.locator(f'a[href="/api/v1/documents/{document_id}/export"]').click()
            downloaded=args.evidence_dir/"browser-export.zip"
            download.value.save_as(downloaded)
            with zipfile.ZipFile(downloaded) as archive:
                assert archive.testzip() is None and "manifest.json" in archive.namelist()
            checks.append("archive preserves history, restore succeeds and export downloads valid ZIP")
            page.locator(f'[data-deletion-document="{document_id}"]').click()
            expect(page.locator('#practiceDeletion [name="confirmation"]')).to_be_visible()
            snapshot("deletion-impact")
            page.locator('#practiceDeletion [name="confirmation"]').fill("wrong confirmation")
            page.locator('#practiceDeletion [type="submit"]').click()
            expect(page.locator("#notice")).to_contain_text("请输入完整")
            page.locator('#practiceDeletion [name="confirmation"]').fill(f"DELETE {document_id}")
            deleted=captured_click('#practiceDeletion [type="submit"]',f"/documents/{document_id}/purge")
            assert deleted.status==202
            expect(page.locator("#practiceDeletion")).to_contain_text("清理完成",timeout=180000)
            expect(page.locator(f'[data-open-attempt="{attempt_id}"]')).to_have_count(0)
            expect(page.locator(f'[data-document="{document_id}"]')).to_have_count(0)
            snapshot("deletion-completed")
            checks.append("deletion requires exact confirmation, completes in background and refreshes history")
            assert not errors, errors
            result["status"]="passed"
            result["external_model_calls"]=0
        except Exception as error:
            result["error"]=str(error)
            snapshot("failure")
            raise
        finally:
            result["page_errors"]=errors
            (args.evidence_dir/"result.json").write_text(json.dumps(result,ensure_ascii=False,indent=2))
            context.tracing.stop(path=args.evidence_dir/"trace.zip")
            browser.close()
    print(f"PASS: {len(checks)} browser checks; evidence: {args.evidence_dir}")


if __name__=="__main__":
    parser=argparse.ArgumentParser()
    parser.add_argument("--base-url",required=True)
    parser.add_argument("--evidence-dir",type=Path,required=True)
    args=parser.parse_args()
    url=urlparse(args.base_url)
    if url.scheme!="http" or url.hostname not in ("127.0.0.1","localhost"):
        parser.error("local isolated API required")
    run(args)
