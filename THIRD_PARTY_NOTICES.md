# Third-Party Notices

O wzap contém lógica adaptada de projetos de terceiros sob as licenças abaixo.

## open-apime/apime

- Origem: https://github.com/open-apime/apime
- Licença: MIT
- Trechos: a semântica de idempotência de envio, a normalização de JID
  (incluindo a regra do 9º dígito brasileiro) e a matemática de humanização
  foram adaptadas deste projeto, reescritas em Go próprio para a arquitetura
  do wzap. A humanização mantém os tetos do estudo (texto ~40 ms por caractere
  com teto de 8 s, áudio com teto de 15 s, mídia por tamanho com teto de 8 s e
  acréscimo de 1,5–2,5 s no primeiro contato), mas não reproduz as micropausas
  de digitação nem o jitter gaussiano.

### Licença

MIT License

Copyright (c) 2026

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
