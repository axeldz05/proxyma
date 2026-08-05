import sys
import json
import os
import glob

# Auto re-exec using parent services-examples .venv/bin/python if invoked with system python
script_dir = os.path.dirname(os.path.abspath(__file__))
services_root = os.path.abspath(os.path.join(script_dir, ".."))
venv_python = os.path.join(services_root, ".venv", "bin", "python")
if os.path.exists(venv_python) and sys.executable != venv_python and "PROXYMA_REEXEC" not in os.environ:
    os.environ["PROXYMA_REEXEC"] = "1"
    os.execv(venv_python, [venv_python] + sys.argv)

for path in glob.glob(os.path.join(services_root, ".venv", "lib", "python*", "site-packages")) + glob.glob(os.path.join("/tmp", ".local", "lib", "python*", "site-packages")):
    if path not in sys.path:
        sys.path.insert(0, path)

import ocrmypdf
from PIL import Image

def main():
    try:
        # Read payload from stdin
        payload = json.load(sys.stdin)
        
        # Extract parameters
        input_path = payload.get("input_path")
        output_path = payload.get("output_path")
        lang = payload.get("lang")
        force_ocr = payload.get("force_ocr", True)
        
        if not input_path:
            print(json.dumps({"error": f"Missing required input_path in payload. Keys: {list(payload.keys())}"}))
            sys.exit(1)
            
        if not os.path.exists(input_path):
            print(json.dumps({"error": f"Input file not found: {input_path}"}))
            sys.exit(1)
            
        if not output_path:
            filename = os.path.basename(input_path)
            output_path = f"/tmp/ocr_{filename}"
            
        extra_args = {}
        if lang:
            extra_args["language"] = lang
            
        if force_ocr:
            extra_args["force_ocr"] = True

        # Convert image inputs to temporary PDF if needed
        input_pdf_path = input_path
        temp_pdf_created = False
        _, ext = os.path.splitext(input_path.lower())
        if ext in [".png", ".jpg", ".jpeg", ".tiff", ".bmp"]:
            img = Image.open(input_path)
            if img.mode != "RGB":
                img = img.convert("RGB")
            temp_pdf_path = f"/tmp/temp_ocr_{os.path.basename(input_path)}.pdf"
            img.save(temp_pdf_path, "PDF")
            input_pdf_path = temp_pdf_path
            temp_pdf_created = True

        ocrmypdf.ocr(
            input_pdf_path,
            output_path,
            progress_bar=False,
            **extra_args
        )

        if temp_pdf_created and os.path.exists(temp_pdf_path):
            try:
                os.remove(temp_pdf_path)
            except Exception:
                pass
        
        print(json.dumps({
            "status": "success",
            "message": "OCR process completed successfully",
            "output_path": output_path
        }))
        
    except Exception as e:
        print(json.dumps({"error": str(e)}))
        sys.exit(1)

if __name__ == "__main__":
    main()
