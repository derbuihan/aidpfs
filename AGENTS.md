# 開発目的

Databricks Workspace 向けの FUSE ソフトウェア `wsfs` を参考に、Oracle AI Data Platform (AIDP) Workbench のワークスペース向け FUSE ソフトウェアを開発する。ワークスペース内のファイルやノートブックをローカルのファイルシステムから扱えるようにすることを目指す。

参照実装は `/home/derbuihan/Programs/wsfs`、AIDP の関連資料は `/home/derbuihan/Programs/oracle` にある。AIDP の API やファイル操作の仕様を確認し、Databricks と同じ動作を前提にせず設計する。

# ドキュメント

- 調査時はまず `docs/` を確認する。公開を前提としない情報も、開発に役立つ内容は `docs/` に記録する。
- `docs/` に置くファイルは `YYYYMMDD_内容が分かる名前.md` とし、先頭の日付には作成日を使う。
- 更新時もファイル名の作成日は変更せず、必要に応じて本文の更新日を更新する。

# Git

- 作業がまとまり、必要な確認が終わったら、適切なタイミングでコミットする。
- 原則として `main` ブランチに直接コミットし、ブランチ運用はシンプルにする。
