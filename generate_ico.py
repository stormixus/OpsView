import cairosvg
from PIL import Image
import io
import sys

def convert_svg_to_ico(svg_path, out_ico_path):
    print(f"Converting {svg_path} to {out_ico_path}...")
    
    # Render SVG to PNG with CairoSVG (preserves transparency by default)
    # We render at a high resolution and then scale down
    png_data = cairosvg.svg2png(url=svg_path, output_width=256, output_height=256)
    
    img = Image.open(io.BytesIO(png_data))
    
    # Save as ICO with multiple sizes
    icon_sizes = [(16, 16), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)]
    img.save(out_ico_path, format='ICO', sizes=icon_sizes)
    print(f"Saved {out_ico_path}")

if __name__ == "__main__":
    if len(sys.argv) < 3:
        print("Usage: python generate_ico.py <input.svg> <output.ico>")
        sys.exit(1)
        
    convert_svg_to_ico(sys.argv[1], sys.argv[2])
