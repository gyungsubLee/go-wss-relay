# TS 클라이언트·공개 제어 API 검증

실행일: 2026-10-22. 작업 저장소: go-wss-relay, codex/wss-v2.

## 참조

MinbWebLib의 원격 main은 MinbWebKit 4.0, ee2715f로 이름을 이전했다.
client/MinbWebKit 서브모듈로 고정하며 viewport·공통 SR 디버거·빌드 프로필을 직접 소비한다.
원본 MinbWebLib 체크아웃은 수정하지 않았다. 데모의 게임 모션과 본문은 이 저장소가 소유한다.

## 실행한 검사

- TS production build: 타입 검사와 Vite build 통과.
- 실제 서버 대상 npm run check: 두 SDK 소켓의 상태 교체·이벤트·접수 확인·세대 증가·접속권 재발급·앱 중단/복귀 통과.
- Go→TS→Go wire check: ClientEnvelope 6종, ServerEnvelope 11종 전체 17종 통과.
- uint64 최대값과 2^53 초과 시각을 bigint로 유지했다.
- Go 경쟁 상태 검사: controlv2, playerauth, relayv2, cmd/wss-relay 통과.
- HTTP 검사: 타인 방·매칭표 접근 거부, 개인 접속권만 반환, 소유자만 시작, FIFO, 취소, 인증 전 CORS preflight 통과.
- 재발급 검사: 이전 티켓 무효화, 세대 교체, 유예 만료 후 재발급 거부 통과.
- 고정 인증 키를 사용하는 배정기 재시작 후 플레이어 토큰 검증 통과.

## 실제 Chrome 두 창

별도 두 탭 1490579144와 1490579145에서 게스트 참가자를 각각 생성했다.
첫 창에서 방 l-uqJ-SfYSwsH9pw2D를 만들고 둘째 창에서 코드로 참가했다.
두 참가자가 준비한 뒤 소유자가 시작했다. 두 참가자 모두 g1로 연결했다.
첫 창의 이동 입력 뒤 둘째 창에서 원격 좌표 0.50, 0.50을 확인했다.
첫 창의 이벤트 접수 확인과 둘째 창의 수신 기록을 확인했다.
둘째 창에서 이벤트를 보내고 재접속하여 g2로 복귀했다.
뱀 몸통을 포함한 전체 상태와 상대의 보간 렌더링을 확인했다.
소유자가 퇴장하자 상대 창에서 공용 상태 권한자의 경기 종료를 확인했다.
화면 증거: client-control-chrome.png.

## 남은 검증

데스크톱 Chrome은 Android WebView 실기기 검증을 대신하지 않는다.
APK/AAB 빌드·실기기·네트워크 전환, 부하·수용량, 노드 분산은 이 증거에 포함하지 않는다.
ready/revision·공개 목록·퇴장·소유자 이전은 제어 API 회귀 검사로 확인했다. 전체 남은 조건은 completion-audit.md를 따른다.

## 최종 빠른 매칭 재검증

Go 1.26.10 노드와 최종 SDK에서 두 Chrome 탭의 빠른 매칭을 실행했다.
방 l-7qwDdoFpfmbBPPA_에서 둘째 참가자는 02:23:31, 소유자는 02:23:32에 연결했다.
예약 참가자가 아직 Welcome에 없다는 이유로 종료하지 않고 두 참가자가 정상 합류했다.
소유자의 0.50, 0.50 좌표와 전체 몸통을 상대가 표시했다.
양방향 이벤트 접수·수신과 둘째 참가자의 g2 복귀를 확인했다.
화면 증거는 client-quickmatch-chrome.png다. 로비 준비·시작 증거와 구분한다.
