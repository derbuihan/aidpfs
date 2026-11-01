# Future Tasks (aidpfs)

Oracle AI Data Platform (AIDP) Workbench の workspace をローカルから操作する FUSE ソフトウェアを開発する。wsfs のチェックリスト形式で作業と結果を管理する。

- `[ ]` は未完了、`[x]` は完了。完了項目は日付・実施環境・検証結果を添えて「完了」へ移す。実装、mock、実 API、実 mount、Workbench UI の成功はそれぞれ別に記録する。
- 最初は Linux・単一ユーザー・1 workspace の指定サブツリーを読み取り専用で実現し、書き込みは API の保証を実測してから追加する。Volume、複数 workspace、共有 mount は初期対象外。
- 既存の資産を壊さない専用テスト領域を使い、作成・削除の対象を限定する。認証情報、PAR、ファイル内容をログや公開資料に残さない。
- 実装上の判断は [AIDP API](https://docs.oracle.com/en/cloud/paas/ai-data-platform/aiwap/rest-endpoints.html)、[FUSE 操作仕様](https://libfuse.github.io/doxygen/structfuse__lowlevel__ops.html)、[Linux FUSE I/O](https://docs.kernel.org/5.19/filesystems/fuse-io.html) および [調査メモ](docs/20261101_実装タスク追加調査.md) を参照する。記載された API の存在は対象テナンシでの動作保証ではない。

---

## 未対応

### 1. 実環境・読み取り API 調査

- [ ] **環境**: 実際に mount するホストの OS、Go、`/dev/fuse`、mount/unmount 権限、必要なら Docker を確認する。Codex 実行環境との違いを記録する。
- [ ] **接続**: 対象 profile・region・AIDP endpoint・platform ID・workspace key・接続経路と権限を読み取りで確認する。workspace が ACTIVE であることと内容 API が利用可能なことを分けて記録する。
- [ ] **一覧**: `GET /objects` の root・指定 path・直接の子・空ディレクトリ・非ファイル object・権限による結果を確認し、`opc-next-page` を使って複数ページを最後まで取得する。
- [ ] **内容**: 既存の通常ファイルと notebook を対象に `objects` の path と `contents`、PAR の対応を調べ、bytes、format、size、時刻、type、ETag、名前の一致を検証する。directory の contents 一覧は前提にしない。
- [ ] **境界**: 日本語・空白・`%`・`#`・階層区切り、root、長い名前、存在しない path、権限不足、失効した認証、429/5xx を検証できる範囲で確認する。結果には日時・操作・status・request ID・未確認事項を残す。
- [ ] **転送経路**: contents と `downloadFileMeta`/PAR の対象種別、実際の取得手順、期限、サイズ制限、アクセス権を調査する。POST の副作用や追加権限も確認し、読み取りと決めつけない。

### 2. 基盤・backend

- [ ] **ライセンス・構成**: wsfs は GPL-3.0 であることを踏まえ、コードやテストをコピーするか、ライセンス・配布方針を決める。Go module、CLI、バージョン表示、整形・テスト導線を整備する。
- [ ] **設定と認証**: OCI profile、region、endpoint、platform ID、workspace key、remote path、mount point の指定・検証と OCI リクエスト署名を実装する。認証方式と対応範囲は実測結果に基づき明記する。
- [ ] **責務分離**: FUSE に OCI 固有型を持ち込まない backend interface、通常ファイル/notebook/ディレクトリの共通 metadata、fake を設計する。wsfs は interface を持つが FUSE 側に Databricks 固有型への依存もあるため、無条件に移植しない。
- [ ] **読み取り**: 一覧の全ページ取得、属性取得、ファイル内容取得、notebook の表示名と bytes を実装する。パスのエスケープ・remote root からの逸脱防止、実サイズと `stat` の一致をテストする。
- [ ] **エラー・再試行**: HTTP status と OCI error code を errno に対応付け、401/403/404/409/412/429/5xx・timeout・切断をテストする。安全に再試行できる操作だけを対象にし、write の結果不明時や非冪等 POST を自動再実行しない。操作ごとの retry token を確認する。
- [ ] **秘密情報**: 署名ヘッダー、鍵、PAR URL と query、取得内容をログ・エラー・テスト成果物に出さない。PAR 取得先への認証ヘッダー転送、redirect、期限切れを検証する。
- [ ] **単体テスト**: API mock/fake によりページング、特殊 path、形式、認証/権限、エラー、キャンセル・timeout、部分応答を検証する。`go test ./...` は OCI 資格情報とネットワークを必要としない。

### 3. 読み取り専用 FUSE

- [ ] **基本操作**: mount/unmount、指定サブツリー、owner-only、`readdir`/`lookup`/`getattr`/`open`/`read` を実装する。書き込みを拒否し、ファイル・ディレクトリの型と空ファイルを正しく表示する。
- [ ] **属性と挙動**: UID/GID・mode・mtime・size・`statfs` の扱い、未対応操作と errno を明文化する。ファイル名衝突と notebook の正確なサイズを確認する。
- [ ] **キャッシュ**: メタデータ・negative・内容・kernel page cache の関係、TTL と外部変更時の再検証を設計する。識別子の欠落や変更、破損、削除、権限変更、同時取得をテストする。wsfs の TTL を AIDP の保証として扱わない。
- [ ] **I/O モード**: direct I/O または write-through を候補に比較し、ページキャッシュと外部更新の整合性を検証する。writeback cache は外部変更に関する Linux の制約を考慮して選択する。
- [ ] **実 mount**: 実 AIDP と FUSE 対応ホストで `ls`、`find`、`stat`、部分/反復 read、`cat`、バイナリ checksum、notebook と通常ファイル、外部変更後の再読込を確認する。mock 成功だけで完了にしない。

### 4. 書き込み API と仕様の実測

- [ ] **使い捨て領域**: 専用フォルダと安全な cleanup を用意し、成功・失敗・中断後にテストデータだけを除去できることを確認する。
- [ ] **操作対応**: WorkspaceObject と notebook contents/PAR それぞれの create、update、mkdir、rename/move、上書き、delete を通常ファイルと notebook で実測する。Notebook POST は無題作成と説明されるため、指定名での create に必要な手順を確認する。
- [ ] **競合と整合性**: GET と更新時の ETag、`if-match`、`compositeEtag` の互換性、複数 client の同時更新、外部変更、曖昧な timeout 後の状態を確認する。各操作の atomicity と非空ディレクトリの削除挙動を記録する。
- [ ] **転送**: upload PAR の取得から転送・完了までの手順、サイズ上限、期限、失敗後の残存物、上書き時の条件付き保護と再試行を確認する。
- [ ] **notebook**: 読取→保存の往復で cells・metadata・outputs・名前・サイズがどう保持されるか、Workbench の自動保存との競合、UI と REST export の違いを確認する。
- [ ] **書き込み契約**: 上記の証拠から操作ごとの可否、競合時の errno、成功の意味、非対応・制約、復旧方法を決める。保証できない上書き・競合を黙って成功扱いにしない。

### 5. 書き込み FUSE

- [ ] **基本操作**: `create`/`write`/`truncate`、`mkdir`、`unlink`/`rmdir`、`rename` を契約に沿って実装する。サイズ・部分書き込み・空ファイル・上書き・非空ディレクトリと errno をテストする。
- [ ] **rename**: 既存先の置換、`RENAME_NOREPLACE`/`RENAME_EXCHANGE`、異なるディレクトリ間、open/dirty source・destination、inode の寿命を検証する。atomicity を保証できない場合は非対応または制限を明示し、非原子的な処理を原子的成功として返さない。
- [ ] **保存と復旧**: dirty buffer と同一ファイルへの並列操作を設計し、flush の複数回/未呼出、fsync、release、shutdown、通信断・競合時の保持と復旧をテストする。release のエラーは close に返らないことを前提に、利用者が失敗を検出・回復できる手段を実装する。`fsync` に安易に `ENOSYS` を返さない。
- [ ] **キャッシュ整合**: 書き込み・rename・削除後の metadata/content/kernel cache、dirty 中の `stat`、他 client の変更を検証する。失敗時に旧データや dirty データを誤って破棄しない。
- [ ] **互換操作**: `chmod`/`chown`、時刻変更、`statfs`、open 後 unlink、forget、locks、非対応 flags の動作を定義し、成功/no-op/errno を明示する。分散 lock をサポートすると未検証のまま主張しない。
- [ ] **容量と障害**: 大きなファイルのメモリ・一時ファイル容量、転送中断、プロセス終了、ローカルディスク不足、再起動後の recovery を検証する。

### 6. 統合試験・公開

- [ ] **実機 suite**: wsfs を参考に、既存 mount 上の FUSE・cache・stress・security suite と、必要な起動 wrapper を作る。破壊的試験は専用領域に限定し、cleanup の成功も確認する。
- [ ] **エディタ E2E**: VSCode 等で開く・編集・保存・再読込・rename を検証し、別 client と Workbench UI で結果を確認する。notebook の内容保持と競合時も対象とする。
- [ ] **Git / 検索**: `git init/status/add/commit` と `rg` の動作・性能を検証し、未対応事項と local separate git dir などの推奨を決める。
- [ ] **セキュリティ**: mount owner 以外のアクセス、キャッシュの owner-only 権限、資格情報・PAR の漏えい、ログ・テスト出力を検証する。
- [ ] **公開準備**: README にセットアップ、認証、利用方法、保証・制約、検証方法、障害復旧を記載し、コード・依存のライセンスと第三者通知を整える。Linux 配布・CI を検証する。`docs/` は公開対象外。

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
