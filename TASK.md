# Future Tasks (aidpfs)

Oracle AI Data Platform (AIDP) Workbench の workspace をローカルから操作する FUSE ソフトウェアを開発する。wsfs のチェックリスト形式で作業と結果を管理する。

- `[ ]` は未完了、`[x]` は完了。完了項目は日付・実施環境・検証結果を添えて「完了」へ移す。実装、mock、実 API、実 mount、Workbench UI の成功はそれぞれ別に記録する。
- 最初は Linux・単一ユーザー・1 workspace の指定サブツリーを読み取り専用で実現し、書き込みは API の保証を実測してから追加する。Volume、複数 workspace、共有 mount は初期対象外。
- 既存の資産を壊さない専用テスト領域を使い、作成・削除の対象を限定する。認証情報、PAR、ファイル内容をログや公開資料に残さない。
- 実装上の判断は [AIDP API](https://docs.oracle.com/en/cloud/paas/ai-data-platform/aiwap/rest-endpoints.html)、[FUSE 操作仕様](https://libfuse.github.io/doxygen/structfuse__lowlevel__ops.html)、[Linux FUSE I/O](https://docs.kernel.org/5.19/filesystems/fuse-io.html) および [調査メモ](docs/20261101_実装タスク追加調査.md) を参照する。記載された API の存在は対象テナンシでの動作保証ではない。

---

## 未対応

### 1. 実環境・読み取り API 調査

- [x] **環境**: 2026-11-02 の現在の実行環境で Ubuntu 26.04.1、Linux 7.0.0-42-generic、Go 1.26.8、`/dev/fuse` の mode 0666・読み書き可能、`fusermount3` を確認。2026-11-01 の実 AIDP read-only mount / unmount 記録も照合。Docker daemon は不要なため確認対象外。隔離された別実行環境では `/dev/fuse` が見えなかった記録と分けて記載。[詳細](docs/20261101_端末環境と調査元.md)。
- [x] **接続**: 2026-11-02、OCI API key profile `OCI_TOKYO` / `ap-osaka-1` / `https://aidp.ap-osaka-1.oci.oraclecloud.com` で対象 platform `aidp-sandbox` と workspace `sandbox` が ACTIVE であることを control-plane / workspace API から別々に確認。workspace key で署名付き objects と既存 FILE・NOTEBOOK contents GET が成功し、読み取り権限を実証。ID/key は記録せず。[詳細](docs/20261101_実APIと実装着手.md)。
- [ ] **一覧**: `GET /objects` の root・指定 path・直接の子・空ディレクトリ・非ファイル object・権限による結果を確認し、`opc-next-page` を使って複数ページを最後まで取得する。
- [ ] **境界**: 日本語・空白・`%`・`#`・階層区切り、root、長い名前、存在しない path、権限不足、失効した認証、429/5xx を検証できる範囲で確認する。結果には日時・操作・status・request ID・未確認事項を残す。
- [ ] **転送経路**: contents と `downloadFileMeta`/PAR の対象種別、実際の取得手順、期限、サイズ制限、アクセス権を調査する。POST の副作用や追加権限も確認し、読み取りと決めつけない。

### 2. 基盤・backend

- [ ] **秘密情報**: 署名ヘッダー、鍵、PAR URL と query、取得内容をログ・エラー・テスト成果物に出さない。PAR 取得先への認証ヘッダー転送、redirect、期限切れを検証する。

### 3. 読み取り専用 FUSE


### 4. 書き込み API と仕様の実測

- [ ] **操作対応**: WorkspaceObject と notebook contents/PAR それぞれの create、update、mkdir、rename/move、上書き、delete を通常ファイルと notebook で実測する。Notebook POST は無題作成と説明されるため、指定名での create に必要な手順を確認する。
- [ ] **競合と整合性**: GET と更新時の ETag、`if-match`、`compositeEtag` の互換性、複数 client の同時更新、外部変更、曖昧な timeout 後の状態を確認する。各操作の atomicity と非空ディレクトリの削除挙動を記録する。
- [ ] **転送**: upload PAR の取得から転送・完了までの手順、サイズ上限、期限、失敗後の残存物、上書き時の条件付き保護と再試行を確認する。
- [ ] **notebook**: 読取→保存の往復で cells・metadata・outputs・名前・サイズがどう保持されるか、Workbench の自動保存との競合、UI と REST export の違いを確認する。

### 5. 書き込み FUSE

- [ ] **rename**: 既存先の置換、`RENAME_NOREPLACE`/`RENAME_EXCHANGE`、異なるディレクトリ間、open/dirty source・destination、inode の寿命を検証する。atomicity を保証できない場合は非対応または制限を明示し、非原子的な処理を原子的成功として返さない。
- [ ] **保存と復旧**: dirty buffer と同一ファイルへの並列操作を設計し、flush の複数回/未呼出、fsync、release、shutdown、通信断・競合時の保持と復旧をテストする。release のエラーは close に返らないことを前提に、利用者が失敗を検出・回復できる手段を実装する。`fsync` に安易に `ENOSYS` を返さない。
- [ ] **互換操作**: `chmod`/`chown`、時刻変更、`statfs`、open 後 unlink、forget、locks、非対応 flags の動作を定義し、成功/no-op/errno を明示する。分散 lock をサポートすると未検証のまま主張しない。
- [ ] **容量と障害**: 大きなファイルのメモリ・一時ファイル容量、転送中断、プロセス終了、ローカルディスク不足、再起動後の recovery を検証する。

### 6. 統合試験・公開

- [ ] **実機 suite**: wsfs を参考に、既存 mount 上の FUSE・cache・stress・security suite と、必要な起動 wrapper を作る。破壊的試験は専用領域に限定し、cleanup の成功も確認する。
- [ ] **エディタ E2E**: VSCode 等で開く・編集・保存・再読込・rename を検証し、別 client と Workbench UI で結果を確認する。notebook の内容保持と競合時も対象とする。
- [x] **Git / 検索**: 2026-11-02、実 AIDP の専用書き込み mount 上の合成 FILE を対象に、別置き Git リポジトリで init/status/add/commit と commit 内容、`rg` を検証。`rg` は単一 FILE で約 4.1 秒。mount 内 `.git`、checkout/merge、複数ファイル性能は未検証。別置き Git dir を推奨。[詳細](docs/20261101_復旧データ基盤.md)。
- [ ] **セキュリティ**: mount owner 以外のアクセス、キャッシュの owner-only 権限、資格情報・PAR の漏えい、ログ・テスト出力を検証する。

---

## 未対応（オプション）

- [ ] API 呼び出し数、検索、並列取得、キャッシュの性能測定と改善。
- [ ] メトリクス、レート制御、運用診断。
- [ ] `--allow-other` と複数ユーザー利用を、認可境界の検証後に検討する。
- [ ] Volume や複数 workspace の対応を検討する。

## 未解決の課題（要検証）

- [ ] WorkspaceObject、notebook contents、PAR の対象・path・ETag の対応とテナンシでの利用可否。
- [ ] 条件付き更新・rename の原子性、timeout 後の確定、dirty データの復旧。
- [ ] notebook の往復保存と Workbench 自動保存の競合。
- [ ] 対象ホストの FUSE mount 権限と実 API・エディタ E2E。

## 既知の制限事項

- 実 AIDP の読み取り API と読み取り専用 FUSE mount は一部確認済み。エディタ E2E は未検証。公開 API 仕様は実動作を保証しない。
- 以前の隔離された Codex 実行環境では `/dev/fuse` が見えなかった。2026-11-01 の今回の実行環境ではデバイスを読み書き可能で、読み取り専用 mount と unmount を確認済み。

## 完了

- 2026-11-02: `scripts/aidp-write-probe.py` が `/Workspace/Shared` のランダム UUID root のみを作成し、既知の合成 child だけを cleanup することを実 AIDP で検証。正常完了後は root DELETE 204 と親一覧からの消滅を確認。途中の曖昧結果では自動削除を止め、journal と候補を残す。実 AIDP PAR/FUSE の process-kill recovery 試験を含む一連の suite 後も root cleanup 済み。[詳細](docs/20261101_書き込みAPI実測.md)、[復旧](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: experimental FUSE の FILE `create`/`write`/`truncate`/`fsync`/`mkdir` と既存 FILE 更新を fake と実 AIDP 専用 mount で検証。部分 write・空 FILE・duplicate create/mkdir の errno と内容、非空 directory delete の remote 危険性を確認し、`unlink`/`rmdir`/`rename` は `EOPNOTSUPP` として拒否する契約を README に明記。rename の flag/置換・同時操作の追加条件は別タスクに残す。[詳細](docs/20261101_復旧データ基盤.md)、[書き込み API 実測](docs/20261101_書き込みAPI実測.md)。

- 2026-11-02: experimental write mount の operation contract を README に整理。create/mkdir の非上書きと intent、FILE-only edit、`fsync`/flush 成功の意味、close が保存しないこと、競合・結果不明時の errno/journal、metadata・delete・rename・statfs 非対応を明示し、fake と実 AIDP fixture の結果へ紐付けた。[README](README.md)、[実測](docs/20261101_書き込みAPI実測.md)、[復旧基盤](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: experimental mount の dirty buffer size を handle/path getattr に反映し、direct I/O で kernel content cache を使わず、clean release 後だけ buffer を破棄、dirty/失敗時は journal を保持することを fake 実 mount で検証。実 AIDP の外部 client 更新では古い版の fsync を `ESTALE` にし dirty data を保持。delete/rename は `EOPNOTSUPP` のため成功後 cache invalidation 経路は提供しない。[復旧・cache 実測](docs/20261101_復旧データ基盤.md)、[書き込み契約](README.md)。

- 2026-11-02: README に setup/authentication、use、limitations、validation、recovery を整理し、GPL-3.0 と imported Go modules のライセンスを [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) に記載。Linux amd64 binary build、gofmt、vet、race suite は現 HEAD で成功。Ubuntu GitHub Actions run 41301673469 も成功（run commit `ae1554f`、現 HEAD より前）。`docs/` は gitignore で公開対象外。[CI](.github/workflows/ci.yml)。

- 2026-11-02: wsfs の GPL-3.0 を確認した上で独立実装とし、プロジェクトの GPL-3.0 方針、Go module、CLI、version、format/test/build 導線を整備。第三者ライセンス通知と配布検証は公開準備タスクに残す。[README](README.md)、[LICENSE](LICENSE)、[Makefile](Makefile)。
- 2026-11-02: OCI profile/region/endpoint/platform/workspace key、remote subtree、mount point の設定・検証と OCI API-key request signing を実装。実 AIDP signed API で接続し、認証対応範囲を API-key profile に限定して記載。[詳細](docs/20261101_実APIと実装着手.md)。
- 2026-11-02: FUSE から OCI 依存を分離した backend interface、通常 FILE/NOTEBOOK/directory metadata と fake backend を実装。API mock のページング・path・format・error・cancel/timeout/部分応答を含む通常 suite は OCI credential/network 不要で実行可能。[詳細](docs/20261101_実APIと実装着手.md)、[再試行と安全性](docs/20261101_読み取り再試行と安全性.md)。
- 2026-11-02: read-only API backend で全ページ列挙、path escaping/root containment、属性・内容・Notebook JSON を実装。mock で size と bytes、欠落/重複名を確認し、実 AIDP の FILE と NOTEBOOK で objects path、contents name/type/format/time、size、bytes、PAR との一致・不一致と ETag 欠落を記録。contents の size が null となる API 制約も反映。[実 API 比較](docs/20261101_実APIと実装着手.md)、[書き込み/PAR比較](docs/20261101_書き込みAPI実測.md)。

- 2026-11-02: OCI status/error code→errno、401/403/404/409/412/429/5xx、timeout/cancel/connection cut, GET-only bounded retry, write/POST outcome-unknown no-retry を mock で確認。Oracle endpoint docs の `opc-retry-token` 対応 endpoint と 24 h/early invalidation を調査し、製品コードが token を送信せず自動 POST retry もしない制限を明記。[詳細](docs/20261101_読み取り再試行と安全性.md)。
- 2026-11-02: read-only FUSE の mount/unmount、指定 subtree、mount owner 限定、readdir/lookup/getattr/open/read と write 拒否を fake 実 mount・実 AIDP で確認。実 AIDP では一覧、find/stat、部分/反復 read、cat/checksum、FILE/NOTEBOOK、外部更新中の既存 handle と再 open を検証済み。小規模の検証で大規模負荷は対象外。[詳細](docs/20261101_実APIと実装着手.md)。
- 2026-11-02: read-only FUSE の UID/GID、mode、mtime、size、unsupported `statfs` と errno を明文化・検証。directory/file mode 0500/0400、size は実 read bytes、notebook JSON size を API bytes と照合、同名 type 衝突を EIO、未対応操作を明示した。kernel page cache を使わない direct I/O と metadata/negative TTL 0 を選び、実 FILE 外部更新で open handle は旧 snapshot、再 open は新内容を確認。[詳細](docs/20261101_実APIと実装着手.md)、[追加調査](docs/20261101_実装タスク追加調査.md)。

- 2026-11-02: read-only FUSE に metadata/entry/negative/disk cache を持たせず、kernel page cache も direct I/O で迂回する方針を確認。外部削除後も既存 handle の snapshot を読め、新規 lookup は `ENOENT`、16 並列 open は各々別 backend read となり内容混同なしを実 mount race test で確認。cache identity/TTL invalidation は適用しない設計である。[詳細](docs/20261101_実APIと実装着手.md)。

- 2026-11-02: remote root 以外の configured subtree も parent の complete listing で存在確認してから mount/`ReadDir` する。missing root は `ENOENT`、実在する empty FOLDER は空一覧として受け付ける。mock で両方を検証し、実 AIDP probe は `/Workspace` 下の既存 subtree で再成功。[詳細](docs/20261101_実APIと実装着手.md)。

- 2026-11-02: read-only fake-backend 実 mount で directory/file mode (0500/0400)、mount owner の UID/GID、mtime、read bytes に一致する size と、write/create/chmod/chown/utimens の `EROFS` を確認。README に POSIX metadata と拒否操作を記載。実 AIDP は別に既存 mount の ls/stat/read を検証済み。[詳細](docs/20261101_実APIと実装着手.md)。

- 2026-11-02: `ReadDir` が objects API の empty 200 を path 存在の証拠にしないよう、子を列挙する前に親の完全な一覧で directory type と存在を検証。存在しない synthetic directory は `ENOENT`、FILE を列挙対象にした場合は `ENOTDIR`。mock と実 AIDP live read probe で確認。[詳細](docs/20261101_実APIと実装着手.md)。

- 2026-11-02: 実 AIDP の読み取り確認を Go の opt-in `TestLiveReadOnlyWorkspace` に追加。現在の専用 sandbox workspace を再帰列挙し、FILE 115 bytes と NOTEBOOK JSON 2065 bytes の表示名/type/path、object と contents の更新時刻、`stat` サイズ、再読込 bytes を照合。どちらも content API は `size: null` / HTTP ETag 無し、objects は `compositeEtag` 無しだった。実データは出力・保存していない。[詳細](docs/20261101_実APIと実装着手.md)。

- 2026-11-02: OCI API の error code を status と併せて errno に対応付ける処理を追加。`NotAuthorizedOrNotFound` は存在と認可を区別せず `ENOENT` にし、`InvalidParameter`、`ResourceLocked`、`NoEtagMatch`、`PayloadTooLarge` などは個別 errno とする。mock の status/code 組合せとエラー文字列への code 非露出を確認。[詳細](docs/20261101_読み取り再試行と安全性.md)。

- 2026-11-02: objects 一覧で同一 `displayName` が異なる type から重複して返る場合、FUSE 上の名前を曖昧にせず `EIO` で一覧を拒否する既存処理に mock regression test を追加。実 AIDP の同名衝突は未観測で、属性項目全体の確認は継続。[詳細](docs/20261101_実APIと実装着手.md)。

- 2026-11-02: AIDP に容量・quota の情報がないため `statfs` の全ゼロ成功を避け、read-only / experimental mount とも `EOPNOTSUPP` を明示。ローカル実 FUSE の双方で `statfs(2)` の errno を確認。名前衝突など属性項目の残りは引き続き未検証。[詳細](docs/20261101_実装タスク追加調査.md)。

- 2026-11-02: create 処理が remote request より前に durable intent を記録し、成功応答後だけ確認 marker を記録することを fake HTTP server と子プロセス強制終了で確認。要求受信後・応答前の終了でも intent が残り、unmount 後検査が未確認として報告。これはローカル模擬サーバーのプロセス停止試験で、実 AIDP の通信中断や電源断は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: 試験的 mount 停止後、新規の状態変更を拒否し実行中の操作を待ってから journal と dirty 状態を検査する gate を追加。ブロックした create の待機・遅延操作拒否を race test、実 AIDP の正常 mount/unmount と cleanup を確認。実通信停止中の終了・強制終了は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: open 後に `fcntl` で O_APPEND を解除しても末尾に書く問題を修正。ローカル実 FUSE で解除・再設定と 2 handle 追記、実 AIDP の専用 FILE で両 flag 変更と REST 内容・cleanup を確認。複数 mount や他カーネルでの原子的追記は未保証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: 子プロセスの `RLIMIT_FSIZE` で journal 実書き込みを EFBIG にし、buffer と実 FUSE の write 拒否、remote 未送信、不完全 entry と先行編集の保持を確認。実 ENOSPC・電源断・実 AIDP 中断は未検証。全体試験で 1 回 FUSE 停滞し、再実行は成功。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: unmount 後に不完全・破損 journal と未解決 snapshot 分岐を検出し、検証済み snapshot の正当な先祖のみ完了済みと扱うよう修正。ローカルで分岐・先祖破損を試験し、実 AIDP の正常保存後の CLI 終了と cleanup を確認。実ディスク満杯・強制終了は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: 実 AIDP の専用 mount 上の合成 FILE を対象に、ローカル別置き Git リポジトリの init/status/add/commit と commit 内容、`rg` を確認。単一 FILE の `rg` は 1 回約 4.2 秒。mount 内 .git、checkout/merge、複数ファイル性能は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: 試験的 mount で生存中の同じ path inode を再利用し、ローカル実 FUSE の stat/fstat inode 不一致を修正。設定を限定した非対話 Vim で実 AIDP の合成 FILE を編集・保存し、REST と再 open で一致、cleanup 済み。通常設定・VS Code・Workbench・遠隔置換時の同一性は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: signal 時の unmount 失敗を表示し、処理を継続して次の signal で再試行するよう修正。mock の失敗→再試行とローカル実 FUSE の signal unmount を確認。実 AIDP の unmount 失敗や in-flight 書き込み時は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: 試験的 CLI の unmount 後、dirty buffer と未確認・不正な create intent を検出した場合にエラー終了する処理を追加。ローカルで状態判定、実 AIDP の正常な CLI mount / unmount・cleanup を確認。強制終了や終了中の in-flight request は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: 同じ移動先への実 AIDP 並行 rename は要求がタイムアウトし成否不明。観測時に合成内容 2 件が残り、手動で専用領域を cleanup。試験的 FUSE の rename は `EOPNOTSUPP` とし、ローカル実 mount で dirty handle の変更保持を確認。原子性と実 mount rename は未解決。[詳細](docs/20261101_書き込みAPI実測.md)。

- 2026-11-02: 実 AIDP の専用 FILE で不正 `if-match` を付けた contents / WorkspaceObject DELETE が各 204 で削除することを確認。試験的 FUSE の unlink / rmdir は `EOPNOTSUPP` とし、実 mount で拒否・対象保持と cleanup を確認。競合安全な削除は未実装。[詳細](docs/20261101_書き込みAPI実測.md)。

- 2026-11-02: 試験的 FUSE で `O_APPEND` を追加。ローカル実 mount で共有 handle の並行追記と同期失敗時 journal 保持、実 AIDP の専用 FILE で `O_APPEND|O_SYNC` と REST 読取・cleanup を確認。別 mount・外部 client の追記競合や open 後の flag 変更は未対応。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: 実 AIDP の専用領域で CLI mkdir の下に FILE を create・write・fsync し、REST 内容と親一覧を確認。子を個別削除後、create intent の `not_listed` と親・専用領域 cleanup を確認。ネスト 1 階層・単一 client の限定試験。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: 試験的 FUSE に非上書き mkdir と FOLDER intent を追加。実 AIDP の専用領域で CLI mkdir、重複拒否、親一覧、`inspect-create` の `directory_listed`、cleanup を確認。ネスト操作・作成中断・rmdir・並行操作は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: `recovery inspect-create` で create intent の対象を親一覧と照合する機能を追加。実 AIDP の専用 FILE で `file_listed` と再送なしを確認し cleanup 済み。未掲載や他種別の実 API 確認・作成成否の確定・自動復旧は未完了。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: FUSE FILE create 前に対象を durable journal に記録し、成功応答の受信を別 marker に保存。`recovery list` で未確認・確認済みを表示。実 AIDP の専用領域で create と記録、cleanup を確認。通信中断・電源断の実環境試験と自動復旧は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: 試験的 FUSE に通常 FILE の create を接続。実 AIDP の専用領域で CLI mount から create・write・fsync・別 API 読取・重複拒否と cleanup を確認。作成結果不明の journal・回復、並行作成やエディタ E2E は未完了。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: 上書き禁止の空 FILE 新規作成 backend を追加。mock で重複・切断・5xx の非再試行、実 AIDP の専用領域で作成・空内容・重複拒否・cleanup を確認。FUSE create、作成結果不明の復旧と複数 client 競合は未実装。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: 試験的書き込み CLI は空の private recovery directory のみ受け付け、起動中は同ディレクトリを排他。ローカル試験で別プロセスのロック、非空時の起動拒否、既存データ保持を確認。別 directory を使う mount 同士や他 client の競合は防がない。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: 既存 FILE 限定の試験的書き込み mount を `--experimental-write` と専用 `--recovery-dir` の組で CLI に接続。実 AIDP の専用合成 FILE で CLI mount、write・fsync・別 API の読取、復元・unmount・cleanup を確認。create/rename/delete、notebook、エディタ、完全な競合保護は未対応。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: 試験用 FUSE の `O_SYNC` / `O_DSYNC` write を遠隔保存・照合の完了まで待つよう修正。ローカル実 mount で両 flag と失敗時 journal 保持、実 AIDP で `O_SYNC` write 直後・close 前の別 API 読取を確認し cleanup 済み。永続性の全保証や truncate の同期 semantics は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: 実 AIDP の専用合成 FILE を試験用 FUSE で dirty にした後、別 client で削除。開いた handle の fstat、path の ENOENT、fsync・close の失敗、journal の内容保持と削除先の不再作成を確認し cleanup 済み。rename や他の競合順序は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: 試験用 FUSE で開いた handle の属性と truncate が remote 属性の再取得に依存しないよう修正。ローカル実 mount / fake で外部削除後の fstat・truncate、同期失敗と journal 内容の保持を確認。実 AIDP の外部削除・rename との競合は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: 試験用 FUSE の読み取り専用 handle の close が、別の書き込み handle の dirty buffer を送信しないよう修正。ローカル実 mount と fake backend で journal 保持・後続 fsync を確認。実 AIDP やエディタでの同条件は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-01: 実 AIDP の専用領域で非空フォルダへの contents DELETE が 204 を返し、フォルダと子が取得できなくなることを確認。`rmdir` の事前空確認だけでは他 client の同時作成を保護できないため、競合安全な実装は未完了。[詳細](docs/20261101_書き込みAPI実測.md)。

- 2026-11-01: 復旧 journal の指定世代から先祖の内容・対象・世代連鎖を検証する `recovery lineage` を追加し、欠損・不整合の拒否をローカルで確認。別分岐の検出や最新版の決定、自動復旧は未実装。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-01: PUT・metadata UPDATE の処理直後に応答を止めたローカル模擬サーバーで client を強制終了し、journal と遠隔状態の照合を確認。実 AIDP でも PUT 成功応答後、journal 更新前に子プロセスを終了し、内容保持と結果不明の分類、合成データ復元・cleanup を確認。通信途中の実環境停止や自動復旧は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-01: OCI_TOKYO / ap-osaka-1 で workspace 一覧、正しい workspace key による objects のページング、contents の既存 notebook・通常ファイルの読み取りを確認。全読み取りタスクの完了ではない。詳細は [実 API 調査](docs/20261101_実APIと実装着手.md)。
- 2026-11-01: Go module、暫定 CLI と backend interface、設定・path の検証を追加。`go test ./...` はネットワーク・OCI 資格情報なしで成功。基盤タスクは未完了。

- 2026-11-01: OCI Go SDK API key 署名の読み取り backend を追加。mock のページング・エラー・特殊 path・形式、実 AIDP の 4 ディレクトリと notebook / FILE 各 3 件の読み取り・stat サイズ一致を確認。当時はリトライ、PAR、FUSE が未実装。詳細は [実 API 調査](docs/20261101_実APIと実装着手.md)。

- 2026-11-01: Linux 実行環境で read-only FUSE の実 AIDP mount / unmount、4 ディレクトリ・6 ファイルの列挙と部分読み取り、存在しないパスの ENOENT、作成拒否 EROFS を確認。外部更新や binary checksum 等は未検証。詳細は [実 API 調査](docs/20261101_実APIと実装着手.md)。

- 2026-11-01: 専用領域で WorkspaceObject のバイナリ作成と contents による読み取り、notebook PUT、cleanup を確認。file PUT は誤った `if-match` でも更新され、競合保護は未確認。[書き込み API 実測](docs/20261101_書き込みAPI実測.md)。

- 2026-11-01: 実 AIDP・FUSE の専用領域で合成 binary の SHA-256、外部更新後の既存 handle と再 open、stat を確認し、unmount・cleanup 済み。1 件の小ファイルに限定。詳細は [書き込み API 実測](docs/20261101_書き込みAPI実測.md)。

- 2026-11-01: 専用 binary の download PAR 取得と署名なし転送、rename の不正 if-match と既存先衝突、WorkspaceObject PUT の条件付き更新を実測。PAR の期限・upload、rename の原子性、競合保護は未解決。[実測詳細](docs/20261101_書き込みAPI実測.md)。

- 2026-11-01: upload PAR の新規 CREATE→署名なし PUT→UPDATE→contents 一致を 7 bytes で確認。重複 CREATE は 409、転送前中断では object が残り削除できた。既存ファイル上書き・障害後の完全な recovery・PAR 有効期限は未検証。[実測詳細](docs/20261101_書き込みAPI実測.md)。

- 2026-11-01: 読み取り GET に限定した一時障害の再試行、timeout、redirect 拒否を追加し mock と `go test -race ./...` で確認。PAR・書き込み・実環境 429/5xx は未検証。[詳細](docs/20261101_読み取り再試行と安全性.md)。

- 2026-11-01: 読み取り専用版の README と Linux CI 設定を追加。現環境で build・test・vet・version を確認。GitHub Actions の実行、配布物と全依存ライセンス通知は未検証。未解決の remote root `/` は CLI で拒否。

- 2026-11-01: 専用 FILE で upload PAR REFRESH が旧内容を保持し、Object Storage 条件付き PUT が不正/有効/旧 ETag に 412/200/412 を返すことを 2 回確認。CREATE + overwrite は転送前に 0 bytes 化。UPDATE の競合・障害回復・notebook は未解決。[実測](docs/20261101_書き込みAPI実測.md)。

- 2026-11-01: REFRESH / UPDATE と条件付き PAR 転送の Go 内部基盤を追加。TLS mock で認証ヘッダー非転送・redirect 拒否・結果不明時の非再試行を確認。実 AIDP・FUSE には未接続、復旧と他 client 競合は未実装。[詳細](docs/20261101_書き込みAPI実測.md)。

- 2026-11-01: Go の PAR metadata・転送を専用合成 FILE で実 AIDP / Object Storage と検証し、条件付き更新・旧 ETag の 412・復元・cleanup を確認。Go POST の署名修正も実施。FUSE 書き込み・同時更新・障害 recovery は未検証。[詳細](docs/20261101_書き込みAPI実測.md)。

- 2026-11-01: 2 client の同時 PAR 条件付き PUT は 2 回とも一方成功・一方 412。PUT 後に別 client が変更してから古い metadata UPDATE を送ると 200 でも別 client の内容が残った。Go PAR GET に ETag の版照合を追加し mock / 実 API で確認。全体の atomicity と recovery は未解決。[詳細](docs/20261101_書き込みAPI実測.md)。

- 2026-11-01: owner-only の復旧内容・状態を fsync / 原子的 metadata 更新で保存する内部基盤を追加。再 Open・破損・symlink・並列状態更新を単体検証。FUSE・通信・再起動 recovery には未接続。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-01: recovery journal と条件付き PAR 保存・更新後照合を内部 coordinator で接続し、fake 障害試験と実 AIDP の専用 FILE で保存・復元を確認。FUSE 接続・中断後の recovery・エディタ E2E は未実装。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-01: 復旧 journal の一覧と上書きしない 0600 export、`put_started` 保存後の子プロセス強制終了・再読を検証。電源断、実通信中の終了、再起動後の自動復旧は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-01: 部分 write/truncate・同期失敗時の freeze・順序付き recovery snapshot を持つ内部 dirty buffer を追加。fake で並列操作・失敗時保持・重複 sync を検証。FUSE 接続、実障害と再起動 recovery は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-01: fake backend と実 FUSE の試験用 mount で部分 write・truncate・共有 handle・fsync・close の失敗伝達と journal 保持を確認。実 AIDP の書き込み mount、create/rename・外部更新・障害復旧は未実装。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-01: 試験用 FUSE と実 AIDP を専用 FILE で接続し write・fsync・別 REST 読取・復元、別 client 更新時の ESTALE と journal 保持を確認。CLI 対応、編集器、rename、障害回復は未完了。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-01: 試験用 FUSE の clean buffer を最後の release 後に破棄し、dirty/失敗 buffer は保持。fake と実 AIDP の専用 FILE で別 client 更新後の再 open を確認。release 前の再 open・他操作の整合性は未解決。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-01: 復旧 journal を版付き PAR snapshot と照合する CLI を追加。mock の分類と、実 AIDP の競合 journal に対する `remote_changed` を確認。自動復旧・他の実分類・Workbench 競合は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-01: 合成 notebook の cells・metadata・outputs と PAR を実測し構造変換を確認。contents PUT で空白名は成功、日本語・%・# は各 400（他経路は未確認）。専用領域 cleanup 済み。[詳細](docs/20261101_書き込みAPI実測.md)。

- 2026-11-02: 実 AIDP 専用 UUID 領域で読み書き FUSE、Vim 編集保存、Git、`rg`、PAR、Notebook REST、rename/delete の境界をまとめて再検証。通常終了時の全 fixture cleanup と親一覧からの消滅を確認。Notebook REST は初回保存で source/metadata/outputs が正規化され、contents PUT や WorkspaceObject PUT の不正 `if-match` も拒否されなかった。詳細と request ID は [書き込み API 実測](docs/20261101_書き込みAPI実測.md)。
