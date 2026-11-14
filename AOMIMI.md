# AOMIMI Network changes

This is a modified version of the Pretendo Network Yo-kai Watch 2 game server, used by AOMIMI Network (a private server for Yo-kai Watch 2: Psychic Specters).
It is not affiliated with, endorsed by, or supported by Pretendo Network, Nintendo or LEVEL-5. Please do not ask Pretendo Network for support with it.

Licensed under the GNU Affero General Public License v3.0, the same as the original (see `LICENSE`).

## Changes (from 2026-11-04)

- Relay (`YW2_RELAY`, `nex/relay.go`): battle traffic between consoles goes through the server, so players behind NAT can battle each other.
- Battle history (`YW2_HISTORY`, `nex/history.go`): records the parties and results of battles that go through the relay, and turns each party into a party code. The data tables are loaded from a separate file (`/history-tables.json`), which is not part of this repository.
- Events (`nex/event_*.go`, `globals/event_*.go`): battles against the event bots from the friends server. Players invite a bot, and the server pairs players who invited the same bot, with optional party rules.
- Event results (`nex/event_result.go`): event battles do not send UploadScore, so the server records the result when a participant's state report shows that one side's six yo-kai are all down. The report is signed with the room's session key, which is checked.
- Account linking bot (`YW2_LINK_BOT`, `nex/event_link.go`, `nex/event_link_notify.go`): a bot used to link a game account with a Discord account. Each player sees their own one-time code as the bot's name; the code is valid only while the bot's invite back is waiting.
- Name filter (`nex/name_filter.go`): in the friend list, strings in other players' names that look like bot names ("E:", "杯:", "認証:", "認証", full or half width) are replaced with "■", so nobody can pretend to be a bot. Stored data is not changed.
- Relay port usage (`nex/relay_status.go`): records how many relay ports are in use, the peak, and how often they ran out.
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
- イベントの勝敗（`nex/event_result.go`）: イベントの対戦ではゲームが UploadScore を送らないので、参加者の状態の報告で片方の 6 体がすべて倒れたら、サーバーが勝敗を記録します。報告の署名（部屋の session key）は確かめます。
- アカウントの結び付けの bot（`YW2_LINK_BOT`、`nex/event_link.go`、`nex/event_link_notify.go`）: ゲームのアカウントと Discord のアカウントを結び付けるための bot です。bot の名前は、見る人ごとにその人だけの使い捨てのキーになります。キーが使えるのは、bot が誘い返している間だけです。
- 名前の置き換え（`nex/name_filter.go`）: フレンド一覧で、ほかのプレイヤーの名前にある bot に見える文字列（「E:」「杯:」「認証:」「認証」。全角・半角は同じ）を「■」に置き換えて、bot になりすませないようにします。保存しているデータは変えません。
- 中継のポートの使い方（`nex/relay_status.go`）: 使っている中継のポートの数、最大、足りなかった回数を記録します。
- パケットは `safely`（`nex/safe.go`）の中で読みます。1 つのパケットの読み取りで想定外の panic が起きても、サーバーを止めずに、そのパケットだけを捨てます。
- `YW2_DEV_*` の設定は試験用で、既定はオフです。
