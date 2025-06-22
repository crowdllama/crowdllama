from ollama import Client
# Local server:
# ollama = Client(host='localhost')
ollama = Client(
   host='http://localhost:9001'
 )
response = ollama.chat(model='tinyllama',stream=False,  messages=[
  {
    'role': 'user',
    'content': 'is the sky blue?',
  },
])
print(response)
