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

- 基盤コードの一部と実 AIDP の読み取り API を確認済み。FUSE mount・エディタ E2E は未検証。公開 API 仕様は実動作を保証しない。
- 以前の隔離された Codex 実行環境では `/dev/fuse` が見えなかった。2026-11-01 の今回の実行環境ではデバイスを読み書き可能だが、実 mount は未検証。

## 完了

- 2026-11-01: OCI_TOKYO / ap-osaka-1 で workspace 一覧、正しい workspace key による objects のページング、contents の既存 notebook・通常ファイルの読み取りを確認。全読み取りタスクの完了ではない。詳細は [実 API 調査](docs/20261101_実APIと実装着手.md)。
- 2026-11-01: Go module、暫定 CLI と backend interface、設定・path の検証を追加。`go test ./...` はネットワーク・OCI 資格情報なしで成功。基盤タスクは未完了。

- 2026-11-01: OCI Go SDK API key 署名の読み取り backend を追加。mock のページング・エラー・特殊 path・形式、実 AIDP の 4 ディレクトリと notebook / FILE 各 3 件の読み取り・stat サイズ一致を確認。リトライ、PAR、FUSE は未実装。詳細は [実 API 調査](docs/20261101_実APIと実装着手.md)。

- 2026-11-01: Linux 実行環境で read-only FUSE の実 AIDP mount / unmount、4 ディレクトリ・6 ファイルの列挙と部分読み取り、存在しないパスの ENOENT、作成拒否 EROFS を確認。外部更新や binary checksum 等は未検証。詳細は [実 API 調査](docs/20261101_実APIと実装着手.md)。
