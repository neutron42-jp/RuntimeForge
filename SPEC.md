# RuntimeForge 仕様書 v0.1

## 1. 目的

LM Studio は upstream llama.cpp のビルド済みランタイムしか配布しないため、upstream が未対応の
新しいモデルアーキテクチャを扱うフォークを実行時に選べない。本プロジェクトは、**任意の Git リポジトリ
（フォーク）を取り込み・ローカルビルドし、モデルに応じて最適なランタイムを自動選択して推論
させる**、LM Studio ライクなローカル推論基盤を提供する。

## 2. スコープ

**やること**

- Web UI + ローカルデーモン（Go）で構成
- Git ソース管理（upstream 既定 + 任意 URL 手動登録）
- UI 画面内での自動ビルド（cmake/ninja、CUDA/Vulkan/CPU）
- ランタイム自動選択（GGUF アーキテクチャ × GPU 検出による静的判定）＋ 手動オーバーライド
- モデルのローカル登録（GGUF スキャン）、明示ロード/アンロード
- OpenAI 互換 API（`model: "loaded"` センチネルでロード済みモデルへ自動ルーティング）
- **ビルド引数を含むほぼ全ての設定をユーザーがオーバーライド可能**（§6）
- Linux（Fedora/Ubuntu）のみ

**やらないこと（非目標）**

- HuggingFace からのモデルダウンロード
- Windows / macOS 対応
- 推論中の実測ベンチマークによるランタイム選択（将来拡張）
- 認証・マルチユーザー（localhost 限定）

## 3. 全体アーキテクチャ

```
┌─────────────────────────────────────────────┐
│  React + Vite UI  (go:embed で同梱)          │
│  127.0.0.1:1234  /            … 管理画面       │
│                  /v1/*        … OpenAI互換    │
│                  /api/v1/*    … 管理API/SSE   │
└───────────────┬─────────────────────────────┘
                │ HTTP (localhost)
┌───────────────▼─────────────────────────────┐
│  runtimeforge (Go 単一バイナリ / デーモン)      │
│  ├ Source Manager   (git fetch/pull)         │
│  ├ Build Engine     (cmake+ninja, ジョブ管理) │
│  ├ Runtime Registry (backend別成果物+manifest)│
│  ├ Hardware Probe   (GPU/backend静的検出)      │
│  ├ Model Registry   (GGUFスキャン/解析)        │
│  ├ Selection Engine (自動判定+手動上書き)      │
│  ├ Model Supervisor (llama-server 子プロセス)  │
│  └ API Gateway      (OpenAI互換 → llama-server)│
└───────────────┬─────────────────────────────┘
                │ 子プロセス (内部ポート)
         ┌──────▼───────┐
         │ llama-server │  … ビルドしたランタイム
         └──────────────┘
```

## 4. 技術スタック

| 層 | 採用 | 備考 |
|---|---|---|
| デーモン | Go 1.26 | 単一バイナリ、`go:embed` で UI 同梱 |
| UI | React + Vite + TypeScript | ビルド成果物を embed。npm 使用（pnpm/yarn 未導入） |
| ビルド | CMake + Ninja | ローカル直接ビルド |
| 設定形式 | TOML | 人間が編集しやすい（JSON も読み込み許容） |
| 状態DB | JSON ファイル（当面） | 将来 SQLite へ差し替え可能なリポジトリ層 |
| API | net/http + 軽量ルータ | OpenAI 互換 + 管理API |
| 進捗通知 | Server-Sent Events | ビルドログ・状態変更を UI へ配信 |

## 5. ディレクトリ構成（アプリ配下に集約）

ルート: `~/.runtimeforge/`（環境変数 `RUNTIMEFORGE_HOME` で上書き可）

```
~/.runtimeforge/
├── config.toml               # 全体設定
├── sources/<name>/           # git チェックアウト（worktree）
├── runtimes/<name>/<commit>/<backend>/   # ビルド成果物 + manifest.json
├── models/                   # 既定スキャン先（追加ディレクトリ可）
├── state/
│   ├── models.json           # モデル登録簿
│   ├── selections.json       # 自動/手動の選択設定
│   ├── overrides.json        # ユーザーオーバーライド（§6）
│   └── hardware.json         # GPU 検出結果キャッシュ
├── logs/
└── cache/
```

## 6. 設定の階層とオーバーライド（重要）

**設計原則: すべての設定・引数は組み込みデフォルトを持ち、ユーザーが任意の階層で上書きできる。**
ハードコードされた値を UI/CLI/API から変更できない状態を作らない。

### 6.1 解決順（後勝ち）

```
組み込みデフォルト
  → config.toml（グローバル）
    → config.d/*.toml（分割設定、任意）
      → ソース別 / バックエンド別オーバーライド（build）
        → モデル別オーバーライド（load）
          → リクエスト時のオーバーライド（API/CLI 引数）
```

各階層は「キーの追加・置換」のみ可能（マージ）。リスト型は既定で置換、`+` プレフィックスで追記を表現する。

### 6.2 オーバーライド可能な対象

| 分類 | 上書き可能な項目 | 設定箇所 |
|---|---|---|
| ビルド | CMake 変数（`-D...` 全て）、generator、build type、追加/除外ターゲット、parallel jobs、環境変数、configure/build 引数そのもの | ソース別・バックエンド別・ビルドジョブ単位 |
| バックエンド | CUDA/Vulkan/CPU の有効可否、優先順位、CUDA arch、`GGML_NATIVE` 等 | グローバル + ビルド単位 |
| 実行 | `llama-server` の全引数（ctx、gpu layers、threads、flash-attn、mmap 等） | モデル別 + リクエスト時 |
| モデル | スキャン先、表示名、既定ロードパラメータ | モデル別 |
| 選択 | フォーマット単位/モデル個別のランタイム指定、自動判定の可否 | 選択設定 |
| サーバ | bind host/port、内部ポート範囲、タイムアウト | グローバル |
| ツールチェイン | 使用する cmake/ninja/nvcc のパス、検出スキップ | グローバル |

### 6.3 表現方法

- **UI**: 各項目は「デフォルト値」を灰色プレースホルダで表示し、編集すると `overridden` マークが付く。「リセット」でデフォルトへ戻す。
- **raw 編集モード**: 各カテゴリで「追加引数を自由入力」できるテキスト欄を用意（例: CMake 追加フラグ、`llama-server` 追加引数）。上級者はここでどんな引数も渡せる。
- **CLI/API**: すべてのオーバーライドを JSON/フラグで指定可能。ビルド・ロード時に上書き値を返し、`overrides.json` に永続化するか一時適用かを選べる。

### 6.4 デフォルト例（`config.toml`）

```toml
[server]
host = "127.0.0.1"
port = 1234
internal_port_range = [20000, 20999]

[runtime]
backend_priority = ["cuda", "vulkan", "cpu"]
max_concurrent_builds = 1

[build]
generator = "Ninja"
build_type = "Release"
parallel_jobs = 0            # 0 = 自動 (CPU数)
# 使用コンパイラ。一部ディストリビューションの既定 gcc（例: gcc 16）は
# llama.cpp / nvcc のビルドに失敗するため、gcc 15 系を指定する例。
# "" でシステム既定。
cc = "gcc-15"
cxx = "g++-15"
# すべてのバックエンドに共通で足す CMake 変数
cmake_defines = { GGML_NATIVE = "ON", LLAMA_BUILD_TESTS = "OFF", LLAMA_BUILD_EXAMPLES = "OFF" }
# 追加の configure / build 引数（自由入力）
extra_configure_args = []
extra_build_args = []

[build.backends.cuda]
enabled = true
cc = ""                      # 空なら [build] を継承
cxx = ""
cmake_defines = { GGML_CUDA = "ON" }
# CMAKE_CUDA_ARCHITECTURES 未指定ならホスト GPU を自動検出

[build.backends.vulkan]
enabled = true
cc = ""
cxx = ""
cmake_defines = { GGML_VULKAN = "ON" }

[build.backends.cpu]
enabled = true
cc = ""
cxx = ""
cmake_defines = {}

[load]
# すべてのモデルに適用される llama-server 既定引数（LM Studio のモデル別設定相当）
extra_args = []
gpu_layers = ""          # -ngl (GPU/CPU オフロード)
threads = 0              # -t (CPU コア数)
context_size = 0         # -c
eval_batch_size = 0      # -b (評価バッチサイズ)
flash_attn = ""          # --flash-attn ("" = auto / "on" / "off" / "auto")
cache_type_k = ""        # --cache-type-k (KVキャッシュ量子化)
cache_type_v = ""        # --cache-type-v
kv_cache_offload = ""    # "" = GPU にオフロード / "off" で --no-kv-offload
load_mode = ""           # --load-mode (auto/none/mmap/mlock/mmap+mlock)
seed = 0                 # --seed
rope_freq_base = ""      # --rope-freq-base
rope_freq_scale = ""     # --rope-freq-scale
cpu_range = ""           # --cpu-range (例 "0-7")

[models]
scan_dirs = ["~/.runtimeforge/models"]

[toolchain]
# 空なら PATH から自動検出
cmake = ""
ninja = ""
nvcc  = ""
```

## 7. Git ソース管理

- 既定ソース `upstream`: `https://github.com/ggml-org/llama.cpp`
- 追加ソース: name / url / ref（branch・tag・commit）を UI から登録
- **更新確認とビルドを分離**:
  - `check`: `git fetch` のみ。remote の新コミットを検出して「更新あり」を記録（ビルドはしない）
  - `build`: 指定 ref/commit をチェックアウトしてビルド
- 自動更新は行わない。UI に「更新確認」ボタンと差分表示
- ソース別にビルド設定（§6）を上書き可能

## 8. ビルドエンジン

- **選択した複数バックエンドを1回のCMake configureでまとめてビルド**し、単一のランタイムを生成
  （例: cuda+vulkan+cpu → `-DGGML_CUDA=ON -DGGML_VULKAN=ON`。llama.cpp が実行時にデバイスを選択）。
  ランタイムのラベルは `cpu+cuda+vulkan` のように正規化した集合。
- 既定コマンド（全て §6 で変更可能）:
  `CC=<cc> CXX=<cxx> cmake -S <src> -B <build> -G <generator> -DCMAKE_BUILD_TYPE=<type> <cmake_defines> <extra_configure_args>`
  → `cmake --build <build> <extra_build_args>`
- コンパイラは `[build].cc/cxx`（バックエンド別に上書き可）で指定。既定 `gcc-15`/`g++-15`
  （既定の新しい gcc では llama.cpp / nvcc のビルドに失敗することがあるため）。空ならシステム既定を使用
- CUDA ビルド時は `CMAKE_CUDA_HOST_COMPILER` を設定中の CXX から自動設定
  （nvcc はホストコンパイラを別判定するため、明示しないと新しい gcc で失敗する）
- ビルド引数はジョブ単位で上書き可能（CMake defines / 追加 configure・build 引数 / CC・CXX /
  generator / build type / 並列数）
- 成果物: `llama-server`（主）, `llama-cli`, 共有ライブラリ, `llama-bench`
- ジョブ管理: 同時ビルド数制限（既定 1・設定可）、**キャンセルは実行中ジョブを即時停止**（1ジョブで
  全バックエンドをビルドするため、待ち行列の取り残しが起きない）、ログを SSE で UI にストリーム
- **ツールチェイン事前チェック**: cmake / ninja / git / gcc / nvcc（CUDA選択時）/ glslc（Vulkan選択時）。
  不足時は Fedora/Ubuntu のインストールコマンドを提示し、検出スキップと手動パス指定も可能

## 9. ランタイム・マニフェスト

ビルド成功時に `runtimes/<name>/<commit>/<backend>/manifest.json` を生成:

```json
{
  "id": "upstream@a1b2c3d/cuda",
  "source": "upstream",
  "git_url": "https://github.com/ggml-org/llama.cpp",
  "commit": "a1b2c3d...",
  "ref": "master",
  "backend": "cuda",
  "built_at": "2026-09-15T12:00:00Z",
  "resolved_cmake_flags": ["-DGGML_CUDA=ON", "-DGGML_NATIVE=ON"],
  "binaries": { "llama-server": "bin/llama-server", "llama-cli": "bin/llama-cli" },
  "supported_architectures": ["llama", "qwen3", "kimi-k3", "gemma4", "..."],
  "host_gpus": ["NVIDIA GeForce RTX 4090"]
}
```

**対応アーキテクチャの抽出**: ビルド後、ソースの `src/llama-arch.cpp` 内 `LLM_ARCH_NAMES` の
文字列を解析して `supported_architectures` に格納。フォークごとの「新アーキ対応」を正確に把握
できる（推測ではない）。`resolved_cmake_flags` にはオーバーライド適用後の実効値を保存する。

## 10. ハードウェア検出と「最速」判定（静的）

- GPU: `nvidia-smi` / `lspci`、Vulkan: `vulkaninfo`
- 静的優先順位（既定）: **CUDA > Vulkan > CPU**（§6 で変更可能）
- 判定結果と利用可能デバイス一覧を `hardware.json` にキャッシュ、UI のダッシュボードに表示
- 将来拡張: `llama-bench` による実測モードを追加できるようインターフェースを分離

## 11. 選択エンジン（自動判定 + 手動オーバーライド）

優先順位:

1. **モデル個別の手動指定**（最優先）
2. **フォーマット単位の指定**（GGUF 等）
3. **自動判定**: ①GGUF の `general.architecture` を読む → ②その arch をサポートするビルド済み
   ランタイムに絞る → ③バックエンド優先順位（CUDA>Vulkan>CPU）→ ④同一 backend なら新しい commit
4. 該当なし: 「arch X に対応するランタイムがない」とエラー表示し、フォーク追加を促す

自動判定そのものもモデル別・フォーマット別に無効化でき、特定ランタイムに固定できる。

## 12. モデル管理

- モデルの登録方法（すべて設定に永続化され、UI/APIから編集可能）:
  - **フォルダ指定＋走査深さ**: `models.roots = [{ path, depth }]`。`depth` は
    ルート直下を 0、1階層下までを 1 … と数え、`-1` で無制限。
  - **個別ファイル指定**: `models.files = ["/path/to/model.gguf"]`。
  - 既定スキャン先 `models.scan_dirs`（深さは `models.scan_depth`）に加え、アプリの
    `models/` を常に含める。
- `*.gguf` を解析し、split シャード（`-00001-of-...`）を1モデルに統合。`clip` は
  projector としてフラグ付けし、単体ロードを禁止
- Go で GGUF ヘッダを直接解析（依存最小）: `general.architecture`, `general.name`,
  `general.size_label`。量子化名はファイル名から抽出（`file_type` はバージョン間で
  ずれるため）
- 明示ロードのみ（JIT/自動ロードなし）。UI または API からロード/アンロード
- **モデル個別のロード設定**を保存可能（`state/model_settings.json`、モデルID単位）。
  LM Studio のモデル別ロード設定に相当する項目を GUI（Models タブの `Config` モーダル）で編集:
  Context `-c`、GPUオフロード `-ngl`、CPUスレッド `-t`、評価バッチ `-b`、Flash Attention
  `--flash-attn`、KVキャッシュ量子化 `--cache-type-k/-v`、**KVキャッシュのGPUオフロード**
  (`--no-kv-offload`)、**ロードモード** `--load-mode`（auto/none/mmap/mlock/mmap+mlock）、
  Seed `--seed`、RoPE `--rope-freq-base/-scale`、CPUアフィニティ `--cpu-range`、
  および**任意のllama.cpp引数をそのまま追記**（`extra_args`）。グローバル既定（`[load]`）に
  モデル個別設定を重ね、ロード時の上書きが最優先。空欄の項目は保存値/既定を使用

## 13. モデルスーパーバイザ

- 選択されたランタイムの `llama-server` を内部ポートで起動し、PID/ポート/状態を管理
- 起動引数は「モデル別設定 → グローバル既定」をマージして生成し、実効コマンドを UI に表示
- アンロード・デーモン終了時に子プロセスを確実に停止
- クラッシュ検知とログ収集

## 14. OpenAI 互換 API

- `GET /v1/models`（登録モデル + ロード状態）
- `POST /v1/chat/completions` / `/v1/completions` / `/v1/embeddings`
- **センチネル `"model": "loaded"`**（および `model` 未指定）: 現在ロード済みモデルへ自動ルーティング。
  クライアント側でモデル名を切り替える手間を排除（LM Studio の課題の解消）
- `model` に未ロードのモデル名を指定: 明示ロード方針のため 409 を返し、ロード API を案内
- 認証なし・`127.0.0.1` バインド限定（bind 先は設定で変更可、ただし既定は localhost のみ）

## 15. 管理 API（`/api/v1`）

| メソッド | パス | 役割 |
|---|---|---|
| GET | `/system` | GPU/ツールチェイン/バックエンド検出結果 |
| GET | `/config` / PUT | 実効設定の取得（デフォルト/上書き/解決後）と更新 |
| GET/POST/DELETE | `/sources` | Git ソースの一覧/登録/削除 |
| POST | `/sources/{id}/check` | 更新確認（fetch のみ） |
| POST | `/sources/{id}/build` | 指定 backend でビルド開始（オーバーライド指定可） |
| GET | `/builds/{job}` | ビルド状態、SSE でログ |
| GET | `/runtimes` | ビルド済みランタイムと対応 arch |
| GET/PUT | `/selections` | フォーマット単位/モデル個別の選択設定 |
| GET/POST | `/models`, `/models/scan` | モデル一覧/スキャン |
| GET/PUT | `/models/sources` | 登録ソース（`scan_dirs`/`roots`/`files`）の取得・置換＋再スキャン |
| GET/PUT/DELETE | `/models/{id}/settings` | モデル個別のロード設定（context/KV量子化/offload/threads/生引数） |
| POST | `/models/{id}/load` / `/unload` | 明示ロード/アンロード（オーバーライド指定可） |
| GET | `/events` | SSE（状態変更・ビルド進捗・**llama-server 推論ログ**） |

SSE のイベント種別: `job` / `log`（ビルドログ行） / `instance` / `server-log`（推論ログ行） /
`models` / `sources` / `config`。

## 16. UI 画面

1. **ダッシュボード**: GPU、利用可能バックエンド、ツールチェイン状態、ロード中モデル、サーバー情報
2. **モデル**: 登録ソース管理（フォルダ＋深さ／個別ファイル）、一覧（ファイル名由来の名前）、
   ランタイム上書き・ロード/アンロード・パラメータ、projector の表示切替
3. **Server**: RuntimeForge API の host/port 設定、llama-server の内部ポート範囲設定、
   モデルのロード/アンロード、**推論ログ（llama-server 出力）のライブ表示**
4. **ランタイム**: ビルド済み一覧（backend/対応arch）、フォーマット単位の既定選択、削除
5. **ソース**: Git URL 追加・更新確認・削除。**Build は1ボタンでダイアログを開き
   cuda/vulkan/cpu をチェックボックスで選んで一括ビルド**。ビルドログ表示
6. **設定**: 実効設定の取得・編集（差分のみ保存）。全項目で上書き＋リセット

## 17. デーモン運用

### 17.1 プロセスモデル（ブートストラッパーは持たない）

**本体である Go デーモン自体が軽量**であり、別途ブートストラッパーを常駐させる構成は採らない。

- 重い処理はすべて必要時に spawn される**子プロセス**に隔離する:
  - 推論: `llama-server`（GPU メモリを消費。未ロード時は存在しない）
  - ビルド: `cmake` / `ninja` / `nvcc`（CPU を消費。ビルドジョブ実行中のみ）
- デーモン本体のアイドル時プロファイル目安: **RSS 10〜30MB / CPU ほぼ 0% / GPU 0MB**。
  静的 UI 配信・SSE 接続・状態ファイル I/O のみで、モデルや GPU を保持しない。
- GPU/ツールチェイン検出は起動時 + 手動再検出のみ。定期ポーリングは既定で無効（有効時も低頻度・設定可）。
- したがって「ブートストラッパーが待機する」のではなく「**軽量デーモンが常駐し、必要になった瞬間に
  重い子プロセスを起動する**」モデルとする。軽量プロセスを二重に常駐させる利得はない。

### 17.2 起動状態の操作

- CLI: `runtimeforge serve|start|stop|status`、`runtimeforge runtime ...`、`runtimeforge model ...`
- 既定はフォアグラウンドの `serve`。`start/stop` はバックグラウンド起動と停止（PID ファイル管理）
- `systemd --user` ユニットのテンプレートを同梱（`systemctl --user enable --now runtimeforge`）
- ユーザーが起動状態を明示的に操作可能。UI にも起動中/停止中の状態を表示
- **子プロセス（llama-server / cmake）は `Pdeathsig` で親の終了に追従**。デーモンが
  SIGKILL されても孤児化しない
- **任意オプション**: 普段プロセスをゼロにしたい場合向けに socket activation
  （systemd ソケット起動、初回 API リクエストで自動起動）を設定で選べるようにする

## 18. 開発フェーズ

| Phase | 内容 |
|---|---|
| 0 | スキャフォールド（Go module / Vite / embed / 設定解決層 / CLI） |
| 1 | Git ソース管理・ビルドエンジン・ツールチェイン検査・ランタイム登録・arch 抽出・GPU 検出 |
| 2 | モデル登録（GGUF 解析）・明示ロード・スーパーバイザ |
| 3 | OpenAI 互換 API・センチネルルーティング・選択エンジン |
| 4 | Web UI 全画面（設定オーバーライド UI 含む） |
| 5 | systemd・チャット・実測ベンチ拡張 |

## 19. 未確定・要確認（推奨デフォルト付き）

1. **アプリルート**: `~/.runtimeforge/`（`RUNTIMEFORGE_HOME` で上書き可）
2. **API 既定ポート**: `1234`（衝突時は設定変更可）
3. **複数モデル同時ロード**: 当面「複数可・`"loaded"` は最後にロードしたモデルへ解決」
4. **設定形式**: TOML（JSON も読み込み許容）
5. **最初にビルドするバックエンド**: 検出 GPU に応じて自動（本環境なら CUDA + Vulkan 両方）
6. **socket activation を使うか**: 既定は無効（軽量デーモンを常駐）。普段ゼロプロセスにしたい場合のみ有効化
