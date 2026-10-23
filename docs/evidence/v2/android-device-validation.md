# Android 실기기 검증 절차

상태: 미실행. 2026-10-22 확인한 adb 기기 목록은 비어 있었다.
이 절차는 원래 계획의 3·4단계 완료 조건을 실행하기 위한 준비다. 빌드 성공으로 실기기 성공을 대신하지 않는다.

## 같은 서버에 연결

현재 브라우저 데모의 기본 제어 서버는 8090이고, 로컬 시험 APK의 API는 https://localhost:9443/v2/api다.
두 서버는 별도 메모리를 사용한다. 서로 다른 서버에서 만든 방 코드를 교환하면 같은 방에 들어갈 수 없다.
실기기 시험에서는 웹과 앱의 VITE_RELAY_API_URL을 같은 배정기로 지정한다.
배정 응답의 websocketUrl도 두 클라이언트에서 도달할 수 있어야 한다.

Wi-Fi·이동통신 전환에는 공개 HTTPS·WSS 검증 주소를 사용한다.
단일 호스트는 `infrastructure/ops/Caddyfile.standalone`과 `infrastructure/config/standalone.env.example`을 사용한다.
`infrastructure/config/caddy.env.example`의 도메인을 별도로 설정한다. [운영 절차](../../../infrastructure/docs/operations.md)를 따른다.
분산 구성은 기존 coordinator·relay 설정을 사용한다. 배정기가 반환한 모든 노드 주소가 두 망에서 도달해야 한다.
Caddy 공개 인증서와 DNS가 실제로 준비됐는지 먼저 확인한다. 설정 파서 통과는 공개 접속 성공의 증거가 아니다.

주소가 준비되면 client에서 아래 환경 값을 사용한다. 예시 도메인은 실제 검증 주소로 바꾼다.

```sh
VITE_RELAY_API_URL=https://staging.example.com/v2/api npm run android:sync
VITE_RELAY_API_URL=https://staging.example.com/v2/api npm run dev
```

기존 Vite가 실행 중이면 먼저 해당 검증 서버를 종료한 뒤 새 환경으로 시작한다.
Android는 동기화 후 JDK 21·Android SDK로 assembleDebug를 다시 실행한다.
웹 Origin과 앱 Origin https://localhost를 서버 허용 목록에 포함한다.
관리 포트 8091과 운영자 토큰은 클라이언트에 공개하지 않는다.

## USB 로컬 시험과 실제 망 시험

로컬 APK의 localhost:9443은 기기 자체를 가리킨다. USB 시험에는 명시적인 포트 전달이 필요하다.

```sh
RELAY_ADB="/Applications/Unity/Hub/Editor/6000.0.26f1/PlaybackEngines/AndroidPlayer/SDK/platform-tools/adb"
"$RELAY_ADB" devices -l
"$RELAY_ADB" -s SERIAL reverse tcp:9443 tcp:9443
"$RELAY_ADB" -s SERIAL install -r _Build/android/2026-10-22-debug/RelayLab-debug.apk
"$RELAY_ADB" -s SERIAL shell am start -n games.storio.relaydemo/.MainActivity
```

SERIAL은 현재 허용된 실제 기기를 선택한다. 기기가 없거나 unauthorized 상태면 설치·실행을 진행하지 않는다.
로컬 APK는 자기 디버그 변형에만 시험 CA를 포함한다. 인증서 유효기간과 APK의 CA 일치를 확인한다.
데스크톱 Chrome은 그 CA를 자동 신뢰하지 않는다. 브라우저 인증서 경고를 자동 우회하지 않는다.
공개 인증서를 사용하는 검증 주소는 이 로컬 CA 절차가 필요 없다.

USB 포트 전달로 성공한 결과는 WebView·메시지·앱 복귀 시험으로 기록한다.
실제 Wi-Fi·이동통신 전환 시험으로 기록하지 않는다. 해당 시험에는 공개 주소로 다시 빌드한 앱을 사용한다.
실제 망 시험 전에는 아래 명령으로 USB 전달을 제거한다.

```sh
"$RELAY_ADB" -s SERIAL reverse --remove tcp:9443
```

AAB는 adb install에 직접 전달하지 않는다. AAB 구조·서명 검사는 check-android-artifacts.py 결과를 사용한다.

## 실행 행렬

| 시험 | 실행 | 확인할 결과 |
|---|---|---|
| 기기 기준 | 모델·Android·WebView 버전·앱 해시·사용 망 기록 | 지원 버전과 실제 Origin, 초기 인증 성공 |
| 로비 교환 | 웹 생성, 앱 참가·준비 후 소유자 시작 | 같은 방 ID, 서로 다른 참가자, 양방향 전체 몸통·좌표·사건 |
| 빠른 매칭 | 양쪽에서 상대 찾기 | 같은 방, 소유자보다 먼저 접속해도 정상 합류 |
| 유예 내 복귀 | 앱 중단 후 상대의 단절 표시를 확인하고 10초 안에 복귀 | 같은 방·참가자와 증가한 세대, 현재 상태 복원 |
| 유예 만료 | 상대의 단절 표시 뒤 40초가 지나서 복귀 | 오래된 접속 권한 거부, 유령 참가자·과거 사건 재생 없음 |
| 화면 잠금 | 앱 실행 중 잠금 후 해제 | 중단·복귀 알림과 실제 단절 시각에 맞는 복구 또는 만료 |
| 프로세스 종료 | 앱 강제 종료 후 재실행 | 새 인증·재입장, 이전 메모리 세션으로 위장하지 않음 |
| Wi-Fi→이동통신 | 공개 주소에서 Wi-Fi를 끄고 이동통신으로 전환 | 단절 후 유예 내 복귀, 세대 교체·상태 복원 |
| 이동통신→Wi-Fi | 같은 조건에서 Wi-Fi로 복귀 | 상대 좌표·사건 교환 재개, 중복 참가자 없음 |
| 저속 기기 | 같은 본문·정원·발표 주기로 별도 기기에서 실행 | 기기 사양·측정 구간·RTT 표본 수·입력과 모션 지연 기록 |
| 권한자 종료 | 소유자 명시 퇴장과 유예 만료를 각각 실행 | 상대가 경기 종료, 임의 권한 이전 없음 |

복귀 유예는 앱 버튼을 누른 시각이 아니라 노드가 단절을 판정한 시각부터 계산한다.
프로세스가 유지된 중단과 실제 프로세스 종료를 구분한다.
디버그 앱은 페이지당 한 매치다. 종료 뒤 새 시험은 앱 재실행 또는 페이지 새로고침으로 시작한다.
화면 잠금 해제와 실제 망 전환은 연결한 기기의 현재 상태에서 확인한다.

## 증거와 완료 판정

각 행의 결과·실제 실행 시각·기기와 WebView 버전·앱 해시·방 ID·세대·관측 지연을 기록한다.
앱 화면과 상대 브라우저 화면에서 같은 교환을 확인한다. 임의의 합격 값을 채우지 않는다.
작은 표본으로 안정적인 p99를 주장하지 않는다. 서버 큐·쓰기 지연과 기기 RTT를 구분한다.
무선 손실률을 측정하지 못했다면 미측정으로 남긴다. TCP 연결 끊기 결과를 무선 패킷 손실률로 바꾸지 않는다.
실행 증거를 확인한 뒤 completion-audit.md와 .planning/STATE.md를 갱신한다.
