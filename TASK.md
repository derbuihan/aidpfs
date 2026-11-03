# Future Tasks (aidpfs)

Oracle AI Data Platform (AIDP) Workbench の workspace をローカルから操作する FUSE ソフトウェアを開発する。wsfs のチェックリスト形式で作業と結果を管理する。

- `[ ]` は未完了、`[x]` は完了。完了項目は日付・実施環境・検証結果を添えて「完了」へ移す。実装、mock、実 API、実 mount、Workbench UI の成功はそれぞれ別に記録する。
- 最初は Linux・単一ユーザー・1 workspace の指定サブツリーを読み取り専用で実現し、書き込みは API の保証を実測してから追加する。Volume、複数 workspace、共有 mount は初期対象外。
- 既存の資産を壊さない専用テスト領域を使い、作成・削除の対象を限定する。認証情報、PAR、ファイル内容をログや公開資料に残さない。
- 実装上の判断は [AIDP API](https://docs.oracle.com/en/cloud/paas/ai-data-platform/aiwap/rest-endpoints.html)、[FUSE 操作仕様](https://libfuse.github.io/doxygen/structfuse__lowlevel__ops.html)、[Linux FUSE I/O](https://docs.kernel.org/5.19/filesystems/fuse-io.html) および [調査メモ](docs/20261101_実装タスク追加調査.md) を参照する。記載された API の存在は対象テナンシでの動作保証ではない。

---

## 実装と検証の状況

### 1. 実環境・読み取り API 調査

- [x] **環境**: 2026-11-02 の現在の実行環境で Ubuntu 26.04.1、Linux 7.0.0-42-generic、Go 1.26.8、`/dev/fuse` の mode 0666・読み書き可能、`fusermount3` を確認。2026-11-01 の実 AIDP read-only mount / unmount 記録も照合。Docker daemon は不要なため確認対象外。隔離された別実行環境では `/dev/fuse` が見えなかった記録と分けて記載。[詳細](docs/20261101_端末環境と調査元.md)。
- [x] **接続**: 2026-11-02、OCI API key profile `OCI_TOKYO` / `ap-osaka-1` / `https://aidp.ap-osaka-1.oci.oraclecloud.com` で対象 platform `aidp-sandbox` と workspace `sandbox` が ACTIVE であることを control-plane / workspace API から別々に確認。workspace key で署名付き objects と既存 FILE・NOTEBOOK contents GET が成功し、読み取り権限を実証。ID/key は記録せず。[詳細](docs/20261101_実APIと実装着手.md)。
- [x] **一覧**: 2026-11-02、OCI API key signer で root・`/Workspace`・4階層の path を全ページ列挙。`opc-next-page` が示す空の最終 page まで追跡し、FOLDER/FILE/NOTEBOOK、直接の子と empty FOLDER を確認。現在の principal の権限で 200 を確認し、別 IAM principal の差は発生させていない。[調査](docs/20261101_実APIと実装着手.md)。
- [x] **境界**: 2026-11-02、root/path/missing、特殊文字の API mock と実書込、失効・401/403・429/5xx の mock、1100文字 path の実 `objects` GET (200 + empty) を確認。200 empty は存在の証拠とせず親 listing を使う。実 IAM 権限差・失効 identity・実429/5xxは検証環境なし。各 request ID / status / 未確認事項を private docs に記録。[調査](docs/20261101_実APIと実装着手.md)、[status mapping](docs/20261101_読み取り再試行と安全性.md)。
- [x] **転送経路**: 2026-11-02、contents と `downloadFileMeta`/PAR の FILE / NOTEBOOK download を確認し、FILE・NOTEBOOK upload PAR も7 byte / synthetic JSON で転送から UPDATE と再 GET まで成功。PAR生成POSTは副作用、署名無しObject Storage転送、現在のprincipalの許可を確認。実FILEでdownload PARを新規生成し、発行直後に使用できて31秒後のRange GETがHTTP 404となる実失効を測定し、応答field・URL queryの期限値不在も確認。30秒間隔の観測で正確なTTLは不明。server max sizeと別権限は未確認、アプリは64 MiB上限。[実測](docs/20261101_書き込みAPI実測.md)、[scope](docs/20261102_完了判定とスコープ.md)。

### 2. 基盤・backend

- [x] **秘密情報**: 2026-11-02、署名 / URL / contents を成果物に含めない実API probe、API/PAR mock redaction、PAR transfer で認証 header/cookie 非転送、redirect 拒否、401 (expiry 模擬) のEACCES・非再試行を確認。新規 download PAR は発行直後に利用でき31秒後のRange GETでHTTP 404となった。生成応答に期限fieldはなく、正確なTTLは未確定。通常のAPI/PAR errorはstatusのみ。[tests](internal/aidp/par_test.go)、[調査](docs/20261101_読み取り再試行と安全性.md)、[AIDP scope](docs/20261102_完了判定とスコープ.md)。

### 3. 読み取り専用 FUSE


### 4. 書き込み API と仕様の実測

- [x] **操作対応**: 2026-11-02、WorkspaceObject / contents / PAR の FILE・NOTEBOOK create/update/overwrite/delete、FOLDER mkdir、contents PATCH / `renameObject` の rename を合成 data で実測。`moveObject` はFILE/NOTEBOOKとも400、Notebook POSTの指定名はその下に Untitled を作る。FUSE契約では危険なmove/deleteを拒否し、指定名Notebookは安全経路を選ぶ。[実測](docs/20261101_書き込みAPI実測.md)、[scope](docs/20261102_完了判定とスコープ.md)。
- [x] **競合と整合性**: 2026-11-02、contents/WorkspaceObject GETにETag・compositeEtagが無い例、誤 `if-match` を受理したPUT/rename/delete、PAR 2 client 条件付きPUTの片方412、Workbench外部更新/stale fsync、timeout後のunknown journal、非空directory deleteを実測。atomic move/deleteは信用できないためFUSEでは拒否。unknown outcomeは再送せずjournal/manual inspectとする。[実測](docs/20261101_書き込みAPI実測.md)、[recovery](docs/20261101_復旧データ基盤.md)。
- [x] **転送**: 2026-11-02、FILE upload PAR 取得→署名無しPUT→ETag付きUPDATE→GETを検証。小サイズ、失敗/重複/overwrite、再試行制御、journalをmock/liveで確認。新規 download PAR は発行から31秒後に Object Storage がHTTP 404で拒否することを実測。AIDP側最大容量は不明、製品上限は64 MiB。unknown writeは非再試行、REFRESH/conditional updateのみ採用。[実測](docs/20261101_書き込みAPI実測.md)、[README](README.md)。
- [x] **notebook**: 2026-11-02、REST synthetic notebook PUT/GET/read-save-read、rich cells/metadata/output差、PAR差、Workbench UI未実行cellの手動保存・rename・再openとsigned GET一致を検証。別の新規 synthetic notebook では手動保存せず2分 auto-save後にsigned GETでmarkerを確認 (`TestLiveNotebookAutosaveMarker`)。さらにUIで未保存編集したまま競合REST PUT (200) を実行し、次のWorkbench周期保存後にsigned GETで古いUI側 marker が再度現れることを照合。自動保存はREST更新を上書きし、競合を検出しなかった。`size=null` / ETagなし。Notebook write-openは非対応とする。[実測](docs/20261101_書き込みAPI実測.md)、[live test](internal/aidp/reader_live_test.go)、[scope](docs/20261102_完了判定とスコープ.md)。

### 5. 書き込み FUSE

- [x] **rename**: 2026-11-02、既存先 rename は実 AIDP で 409 となり双方保持、不正 `if-match` の rename は 200 で移動したが原子性・競合保護を示さなかった。FUSE は通常 rename、`RENAME_NOREPLACE`、`RENAME_EXCHANGE`、未知 flag を一律 `EOPNOTSUPP` とし、dirty source の内容を保持する実 mount test に成功。別ディレクトリ・dirty destination・inode寿命は remote rename 自体が無効のため公開操作としては未対応。[契約](README.md)、[実測](docs/20261101_書き込みAPI実測.md)、[test](internal/mount/experimental_write_test.go)。
- [x] **保存と復旧**: 2026-11-02、dirty共有buffer、複数flush/fsync、close/release、shutdown drain、通信断・競合時のjournal保持、create intent、lineage/inspect/export、CLI異常終了をfake・実FUSE・実AIDPで検証。dirty dataはfreezeし自動再送しない。`fsync`/flushで同期結果を返し、shutdown終了コードとrecoveryコマンドで確認可能。真の電源断/物理disk failureやin-flight実AIDP停止は未保証。[復旧試験](docs/20261101_復旧データ基盤.md)、[README](README.md)。
- [x] **互換操作**: 2026-11-02、read-only mount は属性更新を `EROFS`、experimental mount は `EOPNOTSUPP`、`statfs`・unlink・rmdir・rename は `EOPNOTSUPP` と定義。open 後 unlink も拒否し path / handle を保持、POSIX lock は kernel 内のみ。`forget` は FUSE inode reference の内部 bookkeeping のみで remote に副作用なし。読み取り専用の write/truncate/append flags、experimental append / `O_SYNC`・`O_DSYNC`、rename flags を実 mount で確認。Linux open flags 全種は網羅せず、分散 lock と全旗の意味は主張しない。[contract](README.md)、[tests](internal/mount/readonly_test.go)、[experimental tests](internal/mount/experimental_write_test.go)、[記録](docs/20261101_復旧データ基盤.md)。
- [x] **容量と障害**: 2026-11-02、64 MiB FILE上限とメモリ全量snapshot、毎writeのjournal使用を定義。RLIMIT_FSIZEと満杯にした一時64 MiB tmpfsで実journal書込失敗を起こし、buffer保持・remote未送信・journal状態を実FUSEで確認。subprocess kill後のjournalも検証。電源断・物理disk failureは再現せず、recoveryはmanual。[ENOSPC runner](scripts/tests/run-enospc.sh)、[詳細](docs/20261101_復旧データ基盤.md)、[判定](docs/20261102_完了判定とスコープ.md)。

### 6. 統合試験・公開

- [x] **実機 suite**: 2026-11-02、`scripts/tests/run.sh` を追加し、format/vet/race/build をまとめた。ローカル FUSE、snapshot・writeback、並行 read/write、journal/recovery、permission、PAR redirect/secret redaction tests を実行し成功。`--live` は厳密な UUID 専用 subtree と `/dev/fuse` を要求して、既存 live PAR/FUSE suite を明示実行する。通常 suite 成功は Workbench/E2E 成功を意味しない。[runner](scripts/tests/run.sh)、[validation](README.md)。
- [x] **エディタ E2E**: 2026-11-02、実AIDP experimental FUSE上の合成FILEをVimで編集・保存・再読込しREST GETと照合。rename/unlinkはFUSEが `EOPNOTSUPP` で拒否し、対象を保持することを実mountで確認。Workbench UIの合成Notebookは編集・手動保存・周期auto-save・rename・再openをsigned GETと照合し、外部REST更新を次周期auto-saveが上書きする競合も確認。VS Code自体、Notebook同時書き込みは未検証/非対応。[実測](docs/20261101_復旧データ基盤.md)、[Notebook](docs/20261101_書き込みAPI実測.md)。
- [x] **Git / 検索**: 2026-11-02、実 AIDP の専用書き込み mount 上の合成 FILE を対象に、別置き Git リポジトリで init/status/add/commit と commit 内容、`rg` を検証。`rg` は単一 FILE で約 4.1 秒。mount 内 `.git`、checkout/merge、複数ファイル性能は未検証。別置き Git dir を推奨。[詳細](docs/20261101_復旧データ基盤.md)。
- [x] **セキュリティ**: 2026-11-02、別 UID 65534 から owner-only read-only mount のファイルが読めないことを実 FUSE で確認。recovery directory/files は owner-only で、symlink・他 owner・広い permission を拒否。API key/PAR と contents をログに出さず、PAR origin/redirect と error redaction を mock で検証し、download PAR の実期限切れを発行31秒後のHTTP 404で確認。AIDP の複数 IAM user / permission 差は未検証。標準 logger を使わない製品コードの出力箇所、API/PAR error、CLI最終 error 表示を静的確認し、通常 error はHTTP statusのみ。[詳細](docs/20261101_実APIと実装着手.md)、[tests](internal/mount/readonly_test.go)、[recovery](docs/20261101_復旧データ基盤.md)。

---

## 初期スコープの選択（オプション調査の決定）

- [x] API呼び出し/検索/並列取得を測定: 2026-11-02、親一覧request追加、各open独立read、single-file `rg` 4.1–4.2秒を記録。実 workload baselineなしで改善は行わず、矛盾を避けcacheなしを維持。
- [x] metrics/rate limiting/operations: 初期版はforeground single-user CLI。GETはbounded retryのみで中央管理やmetrics不要と判断。service運用化時の別課題。[判断](docs/20261102_完了判定とスコープ.md)。
- [x] `--allow-other` / multi-user: security boundaryなし、owner-only mount・cross-UID拒否を確認し非対応に決定。[security](docs/20261101_実APIと実装着手.md)。
- [x] Volume / multi-workspace:初期目的がworkspace一個のsubtree。Volumeは別API/権限、multi-workspaceは一mount一workspace keyとし非対応。[判断](docs/20261102_完了判定とスコープ.md)。

## 判断済みの制限事項と外部保証

- [x] WorkspaceObject / contents / PARの対象とpathは対象tenantの synthetic FILE・NOTEBOOKで照合。ETag absent / mismatched endpoint behavior を確認し、Notebook save / moveを安全機能として採用しない。
- [x] 条件付き更新 / rename atomicity は実証されず、timeout後の結果はunknownとしてjournal。dirty dataを保持し、手動recovery。FUSE move/delete拒否。
- [x] notebookのREST/UIのsynthetic往復は検証。WorkBench auto-save競合は未保証でNotebook writeを非対応。
- [x] 対象実行環境の `/dev/fuse` read/write、read-only mount/unmount、write fixture、Vim・Workbench UI・別REST client確認済み。VS Codeと異なるホスト/kernelは未検証。[環境](docs/20261101_端末環境と調査元.md)、[scope](docs/20261102_完了判定とスコープ.md)。

## 既知の制限事項

- 実 AIDP の小規模 read/write FUSE、Vim の合成 FILE、Workbench UI の合成 notebook は検証済み。Workbench の stale REST 更新は1件の合成 notebookで周期auto-saveに上書きされ、競合検出が無い結果だった。VS Code、複数ファイル性能、実電源断は未確認。公開 API 仕様は実動作を保証しない。
- 以前の隔離された Codex 実行環境では `/dev/fuse` が見えなかった。2026-11-02 の今回の実行環境ではデバイスを読み書き可能で、read-only / experimental mount、unmount を確認済み。

## 次期タスク: AIDP 書き込みの安定サポート

### 7. AIDP 書き込み API の再調査と契約確定

- [x] **API全体の対応表**: 2026-11-02、Oracle公式仕様とsandbox tenantのsynthetic実測を、contents POST/PUT/PATCH/DELETE、WorkspaceObject create/update/delete/rename/move、upload/download PAR、Object Storage転送、Notebook export別に比較。FILE/FOLDER/NOTEBOOKのmethod・path・encoding・body/header・status/response・ETag/if-match・work-request・副作用を[private契約表](docs/20261102_AIDP書込みAPI契約.md)に記録し、公式仕様との差異・tenant観測・未測定を区別。URL・署名・内容は保存しない。
- [x] **競合と失敗時の保証**: 2026-11-02、synthetic rootで誤if-match、PAR stale/current条件付きPUT・同時PUT、Workbench外部更新、duplicate create、unknown outcome/journal、retry-token重複、実効期限観測を検証。retry-token replayは初回200・再送500だがobject一つとbytes保持。async work-requestの成功応答、別principal/ACL、最大object/path、実際の容量上限はこのtenantで発生・安全に確認できず、未測定として契約に明記し製品上限64 MiB・OCI API key principalに限定。安全性を担保しないif-match経路は書込み契約に使わない。[契約](docs/20261102_AIDP書込みAPI契約.md)。
- [x] **安全な操作契約**: 2026-11-02、FILE create/update/truncate/append/fsyncとmkdirは非上書きintent、conditional PAR、検証、journalによる契約で固定。rename/unlink/rmdir/Notebook saveは競合保護・復旧を実証できないため `EOPNOTSUPP` / read-only。結果不明は自動再送せずjournalと合成fixtureを照合し、正常run後は専用rootの削除と親一覧からの消失を確認。[README](README.md)、[契約](docs/20261102_AIDP書込みAPI契約.md)。

### 8. 安定した FILE 書き込み

- [x] **API backend**: 2026-11-02、FILE create/update/write/truncate/append/fsyncをconditional Object Storage PARとAIDP metadata completionで実装。失敗・競合はmock/raceと実AIDPで検証し、unknown outcomeは再送せずjournalを残して復旧CLIで照合する。
- [x] **FUSE/POSIX操作**: 2026-11-02、FILE create/open/write/truncate/append/fsync/flush/closeとmkdirを契約に合わせ、VS Codeのopen/search/edit/save/reopen・dirty buffer・外部更新・rename/delete拒否を実AIDP FUSEで確認。rename/unlink/rmdirは安全なAPI保証がないため拒否を維持。
- [x] **experimental表記の解除判定**: 2026-11-02、mock/race/障害、実AIDP API・PAR、実FUSE、複数handle/外部更新、独立GET、journal recovery、cleanup、VS Code E2Eが成功。CLIを `--write` に変更しWARNINGを解除。README/TASKにFILE/FOLDER対応範囲、Notebook/rename/delete未対応、単一writer・64 MiB・unknown outcomeの限界を明記。

### 9. Notebook 書き込みの初回調査（完了、必須機能として再開）

- [x] **保存形式とWorkbench競合**: 2026-11-02、rich synthetic notebookのcontents GETとexport ipynb (HTTP 200) を構造比較。Oracle公開資料はexport `content` をschema上はobject・説明文ではipynb stringと記載。tenant応答はJSON objectでcontents GETとも構造不一致。source/cell metadata/outputsの正規化とPAR JSONとの差がありlossless roundtripは未成立。さらにWorkbench周期autosaveが別client REST PUTを上書きし、wrong if-matchも拒否されなかったためversion conflict controlなしと判定。[契約](docs/20261102_AIDP書込みAPI契約.md)。
- [x] **初回実装判断**: 2026-11-02、contents PUTの誤`if-match`受理、Workbench autosave競合、rich payloadの正規化を確認し、一旦read-onlyとした。ユーザー要件によりNotebook書き込みは必須機能として再開し、以下の新タスクで実現可能な安全境界を確定する。[再調査](docs/20261102_Notebook書き込み再調査.md)。

### 10. VS Code 実行環境と E2E

- [x] **環境の実現可能性**: 2026-11-02、ホストでDocker 29.9.1 daemon・Compose 5.6.0、読み書き可能な`/dev/fuse`、VS Code CLI、OCI configと読取り可能な署名鍵を確認。`wsfs` は `Dockerfile.vscode-test` のNode 20/Xvfb/VS Code test runnerと、ComposeのFUSE device・SYS_ADMIN・privileged containerでE2Eを動かす。AIDP環境もDockerベースで整備可能だが、aidpfsのGo 1.26に合わせたimage、OCI configのread-only mount、AIDP用runnerが必要。wsfsのrunnerは`.env`のDatabricks credentialsを必須とするため、そのままは使わない。
- [x] **AIDP用runner整備**: 2026-11-02、Go 1.26/Node 20/Xvfb/VS Code test runner/FUSE付きDocker Composeを追加。OCI config directoryをread-onlyでbind mountし、API keyは環境変数や成果物に置かない。wsfsのrunner方式を参照した独立構成。[runner](scripts/test-vscode-docker.sh)、[image](Dockerfile.vscode-test)、[compose](docker-compose.vscode.yml)。
- [x] **VS Code E2E**: 2026-11-02、実AIDP UUID synthetic root上のFILEでVS Code open/read/search/edit/save/reopen、dirty buffer保持、外部API更新とreopen、rename/delete拒否を検証。FUSE `fsync`/API GETで保存結果を照合し、対象fixtureを削除後にrootが親一覧から消えたことを確認。Notebookは別途read-onlyと決定。Docker runnerのrace suite/ビルドと実AIDP PAR/FUSE live suiteも成功。

### 11. 必須: Notebook 保存経路と競合境界を確定

- [x] **API資料と追加実測**: 2026-11-02、contents GET/PUTの`hash=1`・誤`if-match`、2-chunk Notebook PUT、Notebook `downloadFileMeta` / `uploadFileMeta(REFRESH/UPDATE)`、PAR conditional PUTを調査。GET hash/ETagなし、誤if-match PUTは200、chunked更新は通常PUTと同じ正規化後NotebookNodeを返し、Notebook PARの古い競合writerは412となり先行内容を保持した。[詳細](docs/20261102_Notebook書き込み再調査.md)。
- [ ] **表現のlossless基準**: contents GET、export ipynb、Notebook download PAR raw bytes、Workbenchで保存・再openした結果を、code/markdown source、cell順序とID、metadata、outputs、attachments、widgets、未知fieldごとに比較する。書込み用の正準表現と意味的同一性の判定基準を決める。内容・PAR URLはログに残さない。
- [ ] **chunk protocolの境界**: Notebook chunked create/updateの実request構造、chunk終端・順序・欠落・重複・SHA不一致・中断後再開・サイズ上限をsynthetic Notebookで検証し、POST/PUTどちらが新規作成・上書きに適するか決める。通常PUTで作成競合時の挙動も確認する。
- [ ] **WorkbenchとPARの競合**: NotebookをWorkbenchで未保存のまま開き、別clientのNotebook PAR conditional save後に周期autosaveする試験を行い、API GET・Workbench表示・PAR versionの三者を照合する。逆順、同時PAR writer、同一・別principalの可能な範囲を試し、UIがCAS/lockを尊重するか結論を記録する。権限変更や別principal作成はこの試験の前提にしない。
- [ ] **保存契約**: 実測に基づき、単一writer条件、Workbench等の非協調writerによる残存競合、fsync成功条件、競合errno、未知結果journal/manual recovery、Notebook作成・rename・deleteの対応範囲を定義する。Notebook書込み必須要件を満たす最小機能と、保証できない挙動をREADME/TASKに記載する。

### 12. 必須: Notebook 書き込みを実装して実環境で受け入れる

- [ ] **Notebook backend**: 保存契約で選んだrepresentationを基に、storage versionを読取り時から保持するNotebook writerを実装する。選択された経路がPARなら `downloadFileMeta` → upload PAR refresh → ETag条件付き転送 → `UPDATE` → 独立readbackを行い、contents PUTの`if-match`をCASとして扱わない。chunked経路を使う場合は順序・hash・未完了uploadの回復を実装する。
- [ ] **FUSEと復旧**: `--write`でNotebookのread/write/truncate/append/fsyncを明示的に有効化し、失敗・412・Workbench競合・結果不明でdirty内容とbase versionをjournalに保持する。Notebook write-openとFILE writeの境界、作成可否、close/fsyncのerrnoをmock/raceで検証する。安全な削除/rename根拠がなければ引き続き拒否する。
- [ ] **実API/UI/Editor E2E**: 合成Notebookで空・rich・output付き・大きいchunked payloadを保存し、contents GET、export、Workbench UI再open、VS Code open/edit/save/reopenで受け入れ基準を照合する。2 client競合、Workbench autosave、412、timeout/process stop後のjournal保持と復旧を実施し、専用root cleanupを確認する。
- [ ] **公開範囲の更新**: すべての受け入れ条件が満たされた後、READMEのNotebook read-only記述、CLI help、TASKのexperimental/write scopeを更新し、利用者向けに競合保証と未保証のwriterを明示する。

### 13. GitHub Actions カバレッジと Linux 最新プレリリース

- [x] **テストカバレッジ**: race付きGo testsでcoverage profileを生成し、Actions summaryに総coverageを表示。profileとsummaryをrunごとの30日artifactとして保存。実機AIDP資格情報・FUSE mountなしでActions run 41562189366 / 41562421233 が成功し、coverageは68.3%。実機E2EはCIに含めない。
- [x] **Linux配布物**: GoReleaserでLinux amd64 / arm64の実行バイナリ入りtar.gz、Debian/Ubuntu向け`.deb`、SHA256SUMSを作成。README・LICENSE・THIRD_PARTY_NOTICES、version、fuse3依存と`/dev/fuse`・OCI profile前提を同梱・説明し、構造・依存・checksumを検証。Actions runner上でamd64 `.deb` のinstall/version/removeも成功。
- [x] **最新プレリリースの置換**: main pushでテスト成功後に生成・検証し、直列化したrelease jobが古い公開releaseを整理して`latest` prereleaseを作成。immutable releaseが存在する場合は削除前に中断する。初回と置換後の公開をActions run 41562189366 / 41562421233で確認。
- [x] **公開後確認**: `latest` は置換を発生させた最新main commitを指す唯一の公開release。GitHubからtar.gz 2件、`.deb` 2件、SHA256SUMSをdownloadし、4成果物のchecksumを再検証。READMEにmoving previewの更新・旧版置換運用を記載。[latest release](https://github.com/derbuihan/aidpfs/releases/tag/latest)。

### 14. 通常 FILE の rename / unlink とディレクトリ操作

Notebook 保存の調査・実装（11–12）は当面保留し、通常 FILE の操作を優先する。2026-11-03 時点の調査では、[WorkspaceObject rename](https://docs.oracle.com/en/cloud/paas/ai-data-platform/aiwap/op-aidataplatforms-aidataplatformid-workspaces-workspacekey-actions-renameobject-post.html)、[contents PATCH](https://docs.oracle.com/en/cloud/paas/ai-data-platform/aiwap/op-aidataplatforms-aidataplatformid-workspaces-workspacekey-notebook-api-contents-contentpath-patch.html)、[WorkspaceObject DELETE](https://docs.oracle.com/en/cloud/paas/ai-data-platform/aiwap/op-aidataplatforms-aidataplatformid-workspaces-workspacekey-objects-objectpath-delete.html) と contents DELETE が候補。先行実測では contents rename と両 DELETE が誤 `if-match` を受理し、非空 FOLDER の contents DELETE で子も見えなくなった。既存 destination の rename は409、moveObject は FILE で400、並行 rename は timeout で原子性不明。[実測](docs/20261101_書き込みAPI実測.md)、[契約](docs/20261102_AIDP書込みAPI契約.md)。これらは対象 tenant の観測であり、他経路・他環境への一般保証ではない。

- [x] **限定 FILE 実装と基本実機検証**: 2026-11-03、`--write` で closed/clean FILE の非置換 rename と unlink を実装。単一 writer・他 client の subtree 変更なしを前提とし、open/dirty は `EBUSY`、既存 destination、rename flags、Notebook、FOLDER mutation は拒否。要求前 intent、単一 API request、成功後一覧・内容照合、結果不明時の後続操作停止と `recovery inspect-mutation` を追加。ホスト実 FUSE + fake backend と対象 sandbox の専用 synthetic root で同一/別 directory rename、内容照合、unlink、open 中拒否、destination 重複拒否を確認し、親一覧による fixture cleanup を確認。race test と実 AIDP VS Code での closed FILE 作成・rename・読取・削除が成功。競合・通信断の実 API 試験、一般 POSIX rename と rmdir は下記のとおり未完了。[詳細](docs/20261103_FILEリネーム削除実装.md)。

- [ ] **API と実測の整理**: renameObject、contents PATCH、moveObject、両 DELETE の対象種別、path/body、競合条件、成功応答と work request の完了判定、再送可否を公式仕様と先行実測で照合。FILE の同一・別ディレクトリ、同名、存在済み destination、型違い、存在しない source、特殊文字、誤/stale `if-match` を経路別に区別し、未確認を残す。特に上書き rename と `RENAME_NOREPLACE` の原子性を事前一覧だけで推定しない。
- [ ] **専用領域での実 API 検証**: workspace 一覧から key を取得し、親一覧で対象を確定した UUID 専用 root の合成 FILE のみで上記未確認ケースを試す。source/destination の独立した一覧・内容・応答・request ID を確認し、同一 destination の並行 rename、外部更新や delete/recreate との競合を個別に実施。timeout・切断・5xx や非同期応答は成否不明として再送せず、両 path を照合する。異常時は自動 cleanup を停止し、所有と内容を確認した対象のみ後片付けする。秘密情報・内容・PAR URL は記録しない。
- [ ] **FOLDER と rmdir の別判定**: 空・非空 FOLDER の rename、両 DELETE の挙動と、空確認後の子作成競合を専用領域で検証。非空削除や競合による子の喪失を防ぐ原子的条件が示せなければ `rmdir` は `EOPNOTSUPP` のままにする。Notebook の rename/delete は FILE と切り分け、検証・契約なしに有効化しない。
- [ ] **公開契約の決定**: 単一 mount・単一 writer の前提と非協調 client の残存リスク、FILE unlink、同一/別ディレクトリ rename、既存 destination の置換、`RENAME_NOREPLACE` / `RENAME_EXCHANGE`、型違い、read-only mount の errno を定義。POSIX と異なる場合は明記し、実現できない操作は拒否する。原子的置換が証明できない限り delete→rename 等を POSIX rename として公開しない。
- [ ] **backend と復旧設計・実装**: 選んだ API の path confinement、種類検証、errno mapping、work request と独立 readback を実装する。破壊的要求前の durable intent に操作・source/destination・観測情報を記録し、timeout・process stop 等の未知結果は再送せず journal と手動照合で扱う。既存 recovery の list/inspect/export・起動時チェックとの互換性と、照合だけでは操作主体や成否を証明できない場合を設計・検証する。
- [ ] **FUSE 実装とローカル試験**: `internal/mount/experimental_write.go` の `Unlink` / `Rename` と必要な backend を契約に沿って接続する。path 単位 buffer、inode、親子 entry、開いた handle、dirty buffer、fsync、同時操作、shutdown の整合性を設計し、扱えない組合せは明示的に拒否する。削除済み open handle の寿命、rename 後の書込み先、destination が open/dirty の場合、再作成、flags、競合と未知結果を mock・race・実ローカル FUSE で検証する。`Rmdir` は別判定を通過するまで拒否する。
- [ ] **実環境・エディタ受け入れ**: 専用 root で FILE の rename/unlink、旧・新 path と独立 API 内容、open/dirty handle、競合、timeout 後の journal 確認を実施。VS Code の通常保存設定と Git 等の代表的操作を試し、原子的置換に依存する操作は対応範囲を明示。正常終了時は既知 fixture のみを削除して親一覧から消失を確認し、結果不明時は cleanup を停止する。mock、ローカル FUSE、実 API、実 AIDP FUSE、エディタを別々の完了条件として記録する。
- [ ] **利用者向け更新**: 検証済みの操作だけ README、CLI help、TASK に反映し、単一 writer 前提、非対応操作、競合・未知結果と復旧手順を記載する。未解決の条件を安全な競合検出や完全な POSIX 互換と表現しない。

## 完了

- 2026-11-03: GitHub Actions run `41562189366`（初回公開）と`41562421233`（既存`latest`の置換）が両方成功。race付き全Go testsのcoverageは68.3%でsummary表示・profile artifact取得を確認。Linux amd64 / arm64のtar.gzと`.deb`、SHA256SUMSを生成し、成果物・依存・checksum検証とUbuntu runner上のamd64 package install/version/removeを通過。2回目のpush後も公開releaseは`latest` 1件だけで、今回commitを指すことを確認。GitHubから5 assetsを取得し、4配布物のchecksumを再検証。[初回Actions run](https://github.com/derbuihan/aidpfs/actions/runs/41562189366)、[置換Actions run](https://github.com/derbuihan/aidpfs/actions/runs/41562421233)、[latest release](https://github.com/derbuihan/aidpfs/releases/tag/latest)。

- 2026-11-02: `scripts/aidp-write-probe.py` が `/Workspace/Shared` のランダム UUID root のみを作成し、既知の合成 child だけを cleanup することを実 AIDP で検証。正常完了後は root DELETE 204 と親一覧からの消滅を確認。途中の曖昧結果では自動削除を止め、journal と候補を残す。実 AIDP PAR/FUSE の process-kill recovery 試験を含む一連の suite 後も root cleanup 済み。[詳細](docs/20261101_書き込みAPI実測.md)、[復旧](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: Workbench `sandbox` UI で専用 synthetic notebook の未実行 cell を編集。手動保存なしの状態で別 client から同じ Notebook へ REST PUT が 200。次の Workbench 周期保存後、UI は「Last saved at」が進み、signed contents API は UI に残っていた stale marker を返した (HTTP 200)。REST client の変更が失われても UI / API は成功を表示し、version/conflict 検出はなかった。この合成データで「Workbench auto-save と外部更新の競合」は再現した。別 notebook でも周期保存後の signed GET は成功。probe は marker / bytes を出力しない。既存 notebook の outputs / metadata 保持は未確認。合成 folder は削除確認で保留中。[詳細](docs/20261101_書き込みAPI実測.md)、[test](internal/aidp/reader_live_test.go)。

- 2026-11-02: experimental FUSE の `chmod`/`chown`/`utimens` と open 後 unlink が実 mount で `EOPNOTSUPP`、対象保持・再読取可能となることを確認。statfs/locks/readonly errno は既存実 mount suite で確認。`forget` は kernel/FUSE の参照解放経路であり POSIX user operation ではなく remote mutation を行わない。操作別 errno と未網羅 flag の範囲を README に記載。[contract](README.md)、[test](internal/mount/experimental_write_test.go)。

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

- 2026-11-02: 実環境の read-only FUSE 上で `fcntl(F_GETLK)` と `fcntl(F_SETLK)` の syscall 成功を確認。これは Linux kernel の mount 内ローカル POSIX advisory lock で、AIDP・Workbench・別 mount との分散 lock ではない。README の対応表を修正し、分散 lock は未対応と明記。互換操作全体の確認は未完了。[詳細](docs/20261101_復旧データ基盤.md)。

- 2026-11-02: read-only FUSE 実 mount test を拡張し、`O_WRONLY`、`O_RDONLY|O_TRUNC`、`O_RDONLY|O_APPEND` がすべて `EROFS` で失敗することを確認。unsupported delete/move は `EOPNOTSUPP`、`statfs` は `EOPNOTSUPP`。`forget` と全 Linux flags の網羅性は未検証。[詳細](docs/20261101_復旧データ基盤.md)。

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
