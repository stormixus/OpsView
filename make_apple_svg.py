import re
import os

def fix_svg(filepath, out_filepath):
    if not os.path.exists(filepath):
        print("Missing file:", filepath)
        return
        
    with open(filepath, 'r') as f:
        content = f.read()

    clip_def = """
    <clipPath id="apple-clip">
        <path d="M512,0 C136.5,0 0,136.5 0,512 C0,887.5 136.5,1024 512,1024 C887.5,1024 1024,887.5 1024,512 C1024,136.5 887.5,0 512,0 Z" />
    </clipPath>
    """
    
    # Insert clipPath into the first <defs>
    if '<defs>' in content:
        content = content.replace('<defs>', f'<defs>{clip_def}', 1)
    else:
        # If no defs, add one after <svg ...>
        content = re.sub(r'(<svg[^>]*>)', r'\1\n<defs>' + clip_def + '</defs>\n', content, count=1)
        
    # Find the exact `<svg ... >` tag
    match = re.search(r'<svg[^>]*>', content)
    if not match:
        print("Could not find <svg> tag in", filepath)
        return
        
    svg_open = match.group(0)
    
    # Split by the match string
    parts = content.split(svg_open, 1)
    if len(parts) < 2:
        return
        
    before_svg = parts[0]
    rest = parts[1]
    
    rest = rest.replace('</svg>', '')
    
    # Build final SVG
    new_content = f'{before_svg}{svg_open}\n<g clip-path="url(#apple-clip)">\n{rest}\n</g>\n</svg>'
    
    with open(out_filepath, 'w') as f:
        f.write(new_content)
        
fix_svg("/Volumes/MacExt/Sites/OpsView/icon-dark.svg", "/Volumes/MacExt/Sites/OpsView/icon-dark-apple.svg")
fix_svg("/Volumes/MacExt/Sites/OpsView/icon-light.svg", "/Volumes/MacExt/Sites/OpsView/icon-light-apple.svg")
print("Successfully generated valid Apple SVG icons!")
