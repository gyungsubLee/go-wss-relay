# WSS fork 작업 상태

- 저장소: https://github.com/gyungsubLee/go-wss-relay
- 원본: game-projects97/go-lobby-relay, 8d80ece
- 작업 브랜치: codex/wss-v2
- 목표: 웹과 Android에서 같은 WSS 메시지 규격을 사용하는 전용 중계 서버.
- 현재 단계: 단일 노드 v2 기반 구현. 전체 리팩터링은 진행 중이다.

구현: Protobuf v2, 접속권 검증, 상태 병합, 이벤트 전달, 재접속, 방 할당 관리 API.
검증 결과와 제한: [실행 안내](../README.md).

다음 작업:
1. TypeScript SDK와 웹 예제를 구현한다.
2. 사용자 인증·매칭 백엔드에 방 할당과 접속권 재발급을 연결한다.
3. 방 소유 노드 배정, 노드 등록, 드레이닝을 구현한다.
4. 전송량 예산과 느린 연결 정책을 부하 시험으로 조정한다.
5. Android WebView 환경에서 재접속과 앱 생명주기를 검증한다.

기존 PROJECT.md, ROADMAP.md와 단계별 증거는 원본의 이력이다.
새 방향은 docs/plans/2026-10-20-web-android-wss-relay-refactor.md를 따른다.
