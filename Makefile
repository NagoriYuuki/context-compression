.PHONY: help verify test race demo demo-full demo-update clean

DEMO_RUN := '^TestCompressionDemo$$'

help:
	@echo "verify      构建、vet、全量测试和竞态检查（提交前一键验收）"
	@echo "test        运行全部自动化测试"
	@echo "race        运行竞态检查"
	@echo "demo        打印五个场景的压缩前后全文"
	@echo "demo-full   在演示基础上附加完整消息元数据、Actions 和诊断"
	@echo "demo-update 场景检查通过后，用 summary 场景重新生成 DEMO.md"
	@echo "clean       删除本地测试产物"

verify:
	go build ./...
	go vet ./...
	go test -count=1 ./...
	go test -race -count=1 ./...

test:
	go test -count=1 ./...

race:
	go test -race -count=1 ./...

demo:
	go test -count=1 -run $(DEMO_RUN) -v

demo-full:
	COMPRESSION_DEMO_FULL=1 go test -count=1 -run $(DEMO_RUN) -v

demo-update:
	UPDATE_DEMO=1 go test -count=1 -run $(DEMO_RUN)

clean:
	rm -f coverage.out
