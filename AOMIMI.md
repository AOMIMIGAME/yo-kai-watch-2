# AOMIMI Network changes

This is a modified version of the Pretendo Network Yo-kai Watch 2 game server, used by AOMIMI Network (a private server for Yo-kai Watch 2: Psychic Specters).
It is not affiliated with, endorsed by, or supported by Pretendo Network, Nintendo or LEVEL-5. Please do not ask Pretendo Network for support with it.

Licensed under the GNU Affero General Public License v3.0, the same as the original (see `LICENSE`).

## Changes (from 2026-11-04)

- Relay (`YW2_RELAY`, `nex/relay.go`): battle traffic between consoles goes through the server, so players behind NAT can battle each other.
- Battle history (`YW2_HISTORY`, `nex/history.go`): records the parties and results of battles that go through the relay, and turns each party into a party code. The data tables are loaded from a separate file (`/history-tables.json`), which is not part of this repository.
- Events (`nex/event_*.go`, `globals/event_*.go`): battles against the event bots from the friends server. Players invite a bot, and the server pairs players who invited the same bot, with optional party rules.
- Packets are read inside `safely` (`nex/safe.go`): an unexpected panic while reading one packet drops that packet instead of stopping the server.
- `YW2_DEV_*` settings are for tests only and are off by default.

---

# AOMIMI Network での変更点

Pretendo Network の妖怪ウォッチ2 のゲームサーバーを、AOMIMI Network（妖怪ウォッチ2 真打 の非公式サーバー）向けに改造したものです。
Pretendo Network、任天堂、レベルファイブとは関係がなく、承認や支援も受けていません。これについて Pretendo Network に問い合わせないでください。

元と同じ GNU Affero General Public License v3.0 で公開しています（`LICENSE`）。

## 変更点（2026-11-04 から）

- 中継（`YW2_RELAY`、`nex/relay.go`）: 本体どうしの対戦の通信をサーバー経由にして、NAT の裏にいるプレイヤーどうしでも対戦できるようにします。
- 対戦履歴（`YW2_HISTORY`、`nex/history.go`）: 中継を通った対戦のパーティと結果を記録し、パーティをパーティコードにします。データの表は別のファイル（`/history-tables.json`）から読み、このリポジトリには含みません。
- イベント（`nex/event_*.go`、`globals/event_*.go`）: フレンドサーバーのイベント対戦管理 bot との対戦です。bot を誘ったプレイヤーどうしを、サーバーが組み合わせます。パーティのルールも付けられます。
- パケットは `safely`（`nex/safe.go`）の中で読みます。1 つのパケットの読み取りで想定外の panic が起きても、サーバーを止めずに、そのパケットだけを捨てます。
- `YW2_DEV_*` の設定は試験用で、既定はオフです。
