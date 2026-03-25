build:
  @echo 'Building'
  @mkdir -p build/
  go build -o build/fittk .

install: build
  @echo 'Installing'
  @echo '{{ style("warning") }}Warning: Edit `justfile` to insert your installation commands.{{ NORMAL }}'
  @echo '{{ style("error") }}Error: Nothing was installed.{{ NORMAL }}'
