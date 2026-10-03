#!/bin/sh
# Cuts the README clips in docs/media from out/pitwall-promo.mp4.
set -eu
cd "$(dirname "$0")"
video=out/pitwall-promo.mp4
clip() {
	ffmpeg -v error -y -ss "$2" -t "$3" -i "$video" -an \
		-vf "fps=15,scale=880:-1:flags=lanczos" \
		-c:v libwebp -q:v 50 -compression_level 6 -loop 0 "../docs/media/$1.webp"
	echo "docs/media/$1.webp"
}
# invariant: start and length in seconds match the scene lengths in src/Promo.tsx.
clip attention 19.7 9.6
clip notify 29.4 3.8
clip drag 33.25 7.6
clip settings 40.9 11.6
clip panemode 52.6 5.7
clip survive 58.4 7.7
