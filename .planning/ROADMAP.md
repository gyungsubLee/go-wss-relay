# WSS 진행 계획

2026-10-23 기준으로 v2 전용 구조와 로컬 검사를 마쳤다. 구조 변경의 최종 리뷰를 마쳤다. 전체 제품 목표는 진행 중이다.
기준은 [원래 WSS 1-5단계 계획](../docs/plans/2026-10-20-web-android-wss-relay-refactor.md)이다.

## 제품 단계

| 단계 | 확인한 범위 | 남은 조건 |
|---|---|---|
| 1. 계약 전환 | Go·TS relay.v2·17종 wire·고정 baseline | Android와 실제 바이너리 교환 |
| 2. 단일 WSS 중계 | 실제 Chrome 로비·FIFO·상태·사건·복귀 | 실제 WebView 관찰 |
| 3. SDK·복구 | epoch 격리·5초 본문 제한·실제 SDK 소켓 | 기기 중단·잠금·망 전환 |
| 4. 전송량 검증 | 2026-10-22 TLS·뱀 본문·TCP 지연 측정 | 저속 실제 장치·무선 망·대상 호스트 수용량 |
| 5. 노드 분산 | 실제 로컬 HTTPS·WSS 6개 시나리오·임시 호스트 보호 검사 | 실제 Linux 운영 배포·원격 CI |

[완료 감사](../docs/evidence/v2/completion-audit.md)는 단계별 증거와 제한을 관리한다.
2026-10-22 부하 측정을 구조 변경 후 성능 보장으로 바꾸지 않는다.

## 구조 변경 작업

[승인한 실행 계획](../docs/superpowers/plans/2026-10-23-go-structure-refactor.md)의 Task 1-9는 구현·검사·개별 리뷰를 마쳤다.
Task 10은 문서·증거·로컬 문서 검사와 독립 리뷰를 마쳤다. 전체 브랜치 리뷰의 I1은 수정 후 재리뷰에서 닫았다.
이 작업은 원래 실기기·망·호스트 완료 조건을 닫지 않는다.

## 다음 행동

구조 변경은 완료했다. 다음 제품 검증은 실제 기기·망·운영 호스트 조건을 준비한다.
실기기가 준비되면 [Android 검증 절차](../docs/evidence/v2/android-device-validation.md)를 실행한다.
운영 호스트 검증은 별도 승인 후 [운영 절차](../infrastructure/docs/operations.md)를 따른다.
