from flask import Flask, jsonify, request

app = Flask(__name__)

@app.route('/')
def index():
    return jsonify({"status": "ml service placeholder"})

@app.route('/predict', methods=['POST'])
def predict():
    data = request.json or {}
    # placeholder prediction
    return jsonify({"prediction": "placeholder", "input": data})

if __name__ == '__main__':
    app.run(host='0.0.0.0', port=5000)
