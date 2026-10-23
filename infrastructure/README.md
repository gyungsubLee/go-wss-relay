# WSS 운영 도구

현재 배포 경로는 `config`, `ops`, `scripts`, `docs`다. 호스트 하나에서 `standalone`, `coordinator`, `relay` 중 한 인스턴스를 실행한다.

- `config`: 운영자가 복사하고 수정하는 설정 예제다. 배포 스크립트는 live 설정을 덮어쓰지 않는다.
- `ops`: 역할별 Caddy 설정, systemd 유닛, 비용 가드다.
- `scripts`: 패키징, 배포, 상태 검사, 토큰 설치, 월별 NIC 전송량 검사다.
- [운영 절차](docs/operations.md): 수동 준비 조건과 배포·복구 절차다.

```sh
infrastructure/scripts/package-release.sh /tmp/wss-release.tar.gz
sudo infrastructure/scripts/deploy-backend.sh /tmp/wss-release.tar.gz FULL_40_CHARACTER_COMMIT_SHA standalone
```

Go 1.26.10으로 Linux amd64 바이너리를 만든다. 대상 호스트는 Ubuntu 24.04, Python 3.12 이상, systemd, Caddy가 필요하다.
배포가 성공해도 재시작 전의 메모리 매치는 복구하지 못한다.
로컬 검사는 임시 디렉터리와 서비스 명령 대체를 사용한다. 실제 Linux 기동·공개 TLS·방화벽 검사를 대신하지 않는다.
