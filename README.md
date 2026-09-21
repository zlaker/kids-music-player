# Детский музыкальный плеер

Один бинарник на Go: веб-сервер на порту **21983**, каталог mp3 как альбомы-папки, общий плеер для всех вкладок.

## Сборка

```bash
make build          # текущая машина
make linux          # Ubuntu amd64
make test
```

## Запуск (не из SSH-сессии)

Не запускайте через `go run` в SSH и не оставляйте на переднем плане: при закрытии консоли процесс умрёт.

На сервере:

```bash
sudo cp bin/kids-music-player-linux-amd64 /usr/local/bin/kids-music-player
sudo useradd --system --home /var/lib/kids-music-player --create-home music || true
sudo mkdir -p /var/lib/kids-music-player/music /var/lib/kids-music-player/state
sudo cp deploy/kids-music-player.service /etc/systemd/system/
# поправьте пути в unit, если музыка лежит в другом месте
sudo systemctl daemon-reload
sudo systemctl enable --now kids-music-player
```

Проверка: `systemctl status kids-music-player` и `http://СЕРВЕР:21983`.

`git pull` сам сервис не обновляет. На сервере один раз пропишите alias и дальше только `kids-update`:

```bash
grep -q 'alias kids-update=' ~/.bashrc || cat >> ~/.bashrc <<'EOF'
alias kids-update='cd /home/alex/Apps/kids-music-player && git pull && go build -o /home/alex/Apps/kids-music-player/kids-music-player ./cmd/kids-music-player && sudo cp deploy/kids-music-player.service /etc/systemd/system/ && sudo systemctl daemon-reload && sudo systemctl restart kids-music-player && sleep 1 && systemctl is-active kids-music-player && curl -sS -m 3 -I http://127.0.0.1:21983/'
EOF
source ~/.bashrc
```

Дальше после каждого пуша:

```bash
kids-update
```

Это: `git pull` → сборка бинарника в путь unit → обновление unit → restart. Успех: `active` и `HTTP/1.1 200`.

## Флаги

```text
kids-music-player -music /path/to/music [-listen :21983] [-state-dir DIR]
```

- `-music` — корень библиотеки, только `.mp3`
- `-listen` — адрес HTTP, по умолчанию `:21983`
- `-state-dir` — плейлисты и история, по умолчанию `~/.kids-music-player`

## Возможности

- список папок и треков с обложками и тегами
- поиск по файлам внутри открытой папки, Enter играет первый результат
- прогресс, перемотка, следующий / предыдущий
- таймер: только стоп музыки
- простые плейлисты
- история проигранного с очисткой
- один общий плеер на все телефоны в сети

Порт без пароля. Не выставляйте его в интернет.
