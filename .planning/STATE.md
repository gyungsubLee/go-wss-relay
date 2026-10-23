# WSS 작업 상태

2026-10-23 기준으로 v2 전용 구조와 로컬 검사를 마쳤다. Task 10 독립 리뷰와 전체 브랜치 리뷰는 대기 중이다.
저장소는 `go-wss-relay`, 브랜치는 `codex/wss-v2`다. 전체 제품 목표는 진행 중이다.

## 현재 확인한 범위

- Task 1-2: ID·운영자 토큰·HTTP 기능을 추출했다. v1 실행·생성·전용 검사와 의존 경로를 제거했다.
- Task 3-5: FIFO 기한·배정 오류·Manager 소유권·app 수명주기·p95 상한 초과 거부를 적용했다.
- Task 5 인증 후속 작업: v1 전용 임의 키 옵션을 제거했다. 활성 토큰 바이트·HMAC 문자열·같은 키 재시작 검증은 유지했다.
- Task 6-7: SDK epoch·HTTP 본문 제한과 데모 조회 복구를 적용했다. 실제 Chrome 두 참가자의 연결·사건·복귀를 확인했다.
- Task 8: v2 staging 생성·drift·고정 baseline gate와 CI 정의를 연결했다. Linux 실행 성공은 아직 확인하지 않았다.
- Task 9: flat 운영 경로·세 역할 배포·토큰·비용 가드를 적용했다. 입력 검사 세 건을 수정하고 개별 재리뷰를 마쳤다.
- 최종 Go race·vet·fmt·govulncheck, TS 빌드·lifecycle·wire 17종·실제 SDK와 HTTPS·WSS 프로세스 검사의 소스 연결을 확인했다.
- Task 10: v1 전용 문서 24개를 제거하고 현재 문서와 증거를 정리했다. 최종 리뷰는 대기 중이다.

현재 명령·출력·소스 해시·Chrome 화면은 [구조 변경 증거](../docs/evidence/v2/2026-10-23-structure-refactor.md)에 있다.
MinbWebKit은 고정 서브모듈이다. MinbWebLib는 이전 문서의 참조 이름이다.

## 남은 조건

실제 Android·화면 잠금·무선 망·저속 장치·대상 호스트 수용량을 아직 검증하지 않았다.
실제 Linux systemd·Caddy·공개 DNS·TLS·방화벽과 원격 CI도 실행하지 않았다.
2026-10-22 Android 빌드·부하 기록은 [완료 감사](../docs/evidence/v2/completion-audit.md)에 보존한다. 전체 부하를 다시 실행하지 않았다.
Chrome은 오래된 실패 조회의 무효화를 확인했다. 성공한 이전 응답을 데모 DOM이 소비하는 경우는 별도 증거가 없다.
SDK deferred 성공 검사와 데모 성공 guard의 코드 리뷰는 이 DOM 검사를 대신하지 않는다.
다음 행동은 Task 10 독립 리뷰와 전체 브랜치 리뷰다.
