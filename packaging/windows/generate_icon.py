"""Generate deterministic application icons for PyInstaller and the tray."""
from pathlib import Path

from PIL import Image, ImageDraw


def create_icon(size: int = 256) -> Image.Image:
    scale = size / 64
    image = Image.new("RGBA", (size, size), (28, 100, 242, 255))
    draw = ImageDraw.Draw(image)
    draw.rounded_rectangle(
        tuple(int(v * scale) for v in (2, 2, 61, 61)),
        radius=int(13 * scale),
        fill=(28, 100, 242, 255),
    )
    draw.line(
        tuple(int(v * scale) for v in (13, 46, 25, 34, 35, 39, 51, 18)),
        fill="white",
        width=max(1, int(5 * scale)),
    )
    for x, height in ((14, 12), (27, 20), (40, 28), (51, 38)):
        draw.rectangle(
            tuple(int(v * scale) for v in (x - 3, 51 - height, x + 2, 51)),
            fill=(255, 255, 255, 215),
        )
    return image


if __name__ == "__main__":
    output = Path(__file__).with_name("stock-analyzer.ico")
    image = create_icon()
    image.save(output, format="ICO", sizes=[(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)])
    print(output)

