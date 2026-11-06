# Indexador PL/SQL → SCIP: diseño de arquitectura

Nov 5, 2026 · @Juanjo

## Objetivo y alcance

**Propuesta:** un indexador en Go, distribuido como binario estático único, sobre la gramática ANTLR de grammars-v4 (target Go), con dos pasadas (declaraciones → resolución), troceado previo por unidades de compilación y emisión SCIP en streaming. El resultado (`index.scip`) da go-to-definition, find-references y navegación entre esquemas en Sourcegraph, y sirve como grafo de código para agentes de IA y análisis de impacto.

**Alcance v1:** paquetes (spec y body), procedimientos y funciones standalone, tipos objeto (spec y body), triggers, vistas, DDL de tablas, secuencias y sinónimos, bloques anónimos y SQL embebido (SELECT, INSERT, UPDATE, DELETE, MERGE). Fuera de v1: Oracle Forms/APEX, Java stored procedures y SQL dinámico no literal.

| Requisito | Objetivo de diseño (a validar en el PoC) |
| --- | --- |
| Rendimiento completo | 1 M LOC en menos de 2 min con 16 núcleos |
| Rendimiento incremental | PR típico en menos de 30 s reutilizando caché |
| Precisión | ≥ 95 % de identificadores resueltos con catálogo completo |
| Robustez | Ningún fichero bloquea el proceso; degradación a indexado léxico |
| Determinismo | Misma entrada → mismo `index.scip` byte a byte |
| Memoria | Pico acotado: sin árbol ANTLR ni índice completo en memoria |

## Modelo SCIP aplicado a PL/SQL

Cada objeto Oracle se identifica por `ESQUEMA.OBJETO[.MIEMBRO]`, y eso se traduce directamente a descriptores SCIP: esquema = namespace, objeto = type, miembro = term o method. El `package` de SCIP identifica la base de datos lógica, no el esquema, para que los índices de varios repos que comparten esquemas se enlacen entre sí.

Formato: `scip-plsql oracle <bd-lógica> <versión> <descriptores>`. Ejemplo: `scip-plsql oracle erp-core 3f2a9c1 HR/EMP_PKG#GET_SALARY(+1).(P_EMP_ID)`.

| Objeto PL/SQL | Descriptor SCIP | Kind en SymbolInformation |
| --- | --- | --- |
| Esquema | `HR/` | Namespace |
| Paquete | `HR/EMP_PKG#` | Package |
| Subprograma de paquete (sobrecargas) | `HR/EMP_PKG#GET_SALARY(+1).` | Function / Method |
| Procedimiento o función standalone | `HR/RECALC().` | Function |
| Parámetro | `…GET_SALARY(+1).(P_EMP_ID)` | Parameter |
| Variable, constante, cursor, excepción de paquete | `HR/EMP_PKG#G_RATE.` | Variable / Constant |
| Tipo de paquete (record, colección) | `HR/EMP_PKG#T_EMP_TAB#` | Type / Struct |
| Tipo objeto y sus atributos/métodos | `HR/ADDRESS_T#`, `HR/ADDRESS_T#STREET.` | Class / Field / Method |
| Tabla o vista y sus columnas | `HR/EMPLOYEES#`, `HR/EMPLOYEES#SALARY.` | Struct / Field |
| Secuencia, sinónimo | `HR/EMP_SEQ.`, `PUBLIC/EMP#` | Variable / TypeAlias |
| Trigger | `HR/TRG_EMP_AUDIT.` | Function |
| Variables locales, índices de bucle, etiquetas | `local N` | Variable |

Reglas de nombres:

- Identificadores sin comillas se normalizan a mayúsculas; los entrecomillados se conservan literalmente y se escapan con backticks cuando SCIP lo requiere.
- Sobrecargas: disambiguador `(+n)` por orden de declaración en la spec (convención de scip-java). Los subprogramas privados del body se numeran después de los públicos.
- Los símbolos locales (`local N`) reducen el tamaño del índice; los parámetros son globales para que el hover muestre la firma completa.

Roles de ocurrencia:

- Spec del paquete o del tipo → `ForwardDefinition`; implementación en el body → `Definition` (mismo patrón que scip-clang con cabecera y .cpp). Subprogramas solo en el body → `Definition`.
- Lado izquierdo de `:=`, destinos de `INTO`, `OUT`/`IN OUT` y columnas en `INSERT`/`UPDATE`/`MERGE SET` → `WriteAccess`; el resto de usos → `ReadAccess`. Esto habilita análisis de impacto sobre columnas.

Relaciones (`Relationship`):

- Body de tipo → spec: `is_implementation`. Subtipo `UNDER` → supertipo: `is_implementation`.
- Variable `%TYPE` / `%ROWTYPE` → tabla o columna ancla: `is_type_definition`.
- Sinónimo → objeto destino: `is_reference`. Las ocurrencias que usan un sinónimo apuntan al objeto resuelto; opcionalmente se emite una segunda ocurrencia sobre el sinónimo.

## Arquitectura general

Pipeline de siete fases en el que solo el catálogo global obliga a esperar a todas las unidades; el resto trabaja unidad a unidad en paralelo y la caché permite saltar fases enteras para lo que no ha cambiado.

&#91;embedded content: pipeline del indexador · 7 fases, 3 entradas\]

Parsing, pasada 1 y pasada 2 se ejecutan en el mismo pool de goroutines trabajadoras (tantas como `GOMAXPROCS`); el escritor es una única goroutine que consume un canal con buffer acotado.

## Ingesta y troceado

El paso que más rendimiento aporta es no entregar scripts enteros al parser: un escáner manual (sin ANTLR, una sola pasada lineal) parte cada fichero en **unidades de compilación** independientes, que luego se parsean en paralelo y aíslan los fallos.

El escáner reconoce:

- Literales (`'…'` y q-quotes `q'[…]'`), comentarios `--` y `/* */`, para no cortar dentro de ellos.
- Terminadores SQL\*Plus: `/` solo en una línea tras un bloque PL/SQL, y `;` a nivel de sentencia SQL fuera de bloques.
- Comandos SQL\*Plus que se descartan: `SET`, `PROMPT`, `SPOOL`, `DEFINE`, `WHENEVER`, `REM`, `EXEC`, `@`/`@@` (estos últimos se registran como dependencia de fichero).
- La cabecera de cada unidad (`CREATE [OR REPLACE] [EDITIONABLE] PACKAGE BODY HR.EMP_PKG`…) para clasificarla y elegir la regla de entrada del parser sin parsear nada.

Cada unidad conserva su offset base en el fichero, de modo que las posiciones SCIP se calculan sin recomponer texto.

Preprocesado que preserva offsets (todo se sustituye por caracteres del mismo ancho):

- **Variables de sustitución** `&var` / `&&var` → identificador ficticio de la misma longitud.
- **Compilación condicional** `$IF … $THEN … $ELSE … $END` → se evalúa con flags configurables (equivalente a `PLSQL_CCFLAGS`) y las ramas inactivas se rellenan con espacios.
- **Código wrapped** → no se parsea; se emite solo la definición del objeto a partir de la cabecera y un diagnóstico.

Extensiones reconocidas por defecto: `.sql`, `.pks`, `.pkb`, `.pls`, `.plb`, `.prc`, `.fnc`, `.trg`, `.typ`, `.tps`, `.tpb`, `.vw`, configurables por glob. Las migraciones Flyway/Liquibase se ordenan por versión para reproducir el DDL en orden.

Codificación: detección de BOM y heurística UTF-8 → Windows-1252/ISO-8859-15 (frecuente en código heredado en España), con transcodificación a UTF-8 vía `golang.org/x/text/encoding`. Como Go trabaja con bytes UTF-8, el índice declara `UTF8CodeUnitOffsetFromLineStart`: las posiciones salen directamente de los offsets de byte sin conversión, y SCIP las traduce para los clientes que esperan UTF-16.

## Parsing con la gramática ANTLR de grammars-v4

La gramática `sql/plsql` (`PlSqlLexer.g4` + `PlSqlParser.g4`) cubre PL/SQL y SQL de Oracle de forma amplia, pero es muy grande y ambigua: su coste está en la predicción adaptativa, no en el lexer. Se usa el target Go (`github.com/antlr4-go/antlr/v4`) con un fork fijado a un commit concreto, adaptado con el `transformGrammar.py` de la carpeta `Go/` de la gramática y con sus clases base `PlSqlLexerBase` / `PlSqlParserBase` en Go. Hay precedentes en producción de este mismo camino, como bytebase/plsql-parser.

**Riesgo principal:** el runtime Go de ANTLR es menos rápido y consume más memoria que el de Java con gramáticas de este tamaño. Por eso las técnicas siguientes no son opcionales, y la puerta de la fase 1 mide el parsing en Go frente a los objetivos antes de seguir. Si no los alcanza, el plan B es adelantar el parser propio tolerante en Go (fase 4) para el camino caliente, manteniendo ANTLR como respaldo.

**Alternativa que se evalúa en paralelo:** `bytebase/omni/oracle`, un parser PL/SQL recursivo descendente en Go puro con AST y offsets por nodo (ver la sección de librerías). Si cubre el corpus, sustituye a ANTLR y las técnicas 1, 3 y 6 de esta lista dejan de aplicar. En lugar de generar el parser ANTLR desde nuestro fork, se parte del paquete `plsql` mantenido en `bytebase/parser`.

Técnicas, de mayor a menor impacto esperado:

1. **Parsing en dos etapas.** Primero `antlr.PredictionModeSLL` con `BailErrorStrategy`; solo si falla, se repite la unidad en modo `LL` con recuperación de errores. La mayoría del código válido se resuelve en SLL.
2. **Regla de entrada por unidad.** El clasificador del troceado llama a `Create_package_body()`, `Create_procedure_body()`, `Create_trigger()`, etc., en lugar de `Sql_script()`, lo que reduce alternativas y ambigüedad.
3. **Un parser por goroutine y DFA compartido.** Lexer y parser se crean por goroutine trabajadora y se reutilizan con `SetInputStream`. El caché DFA generado es global: hay que verificar en el PoC que el runtime Go fijado lo comparte de forma segura entre goroutines (con `-race` y carga real); si no, se usa un caché por trabajador. Al arrancar se parsea un corpus de calentamiento.
4. **Árbol efímero.** El listener/visitor convierte el parse tree en una IR compacta propia (structs planos en slices, offsets `int32`, identificadores internados) y el árbol se suelta inmediatamente para que el GC lo recoja en bloque.
5. **Saltar lo irrelevante.** `GRANT`, `ALTER SESSION`, `ANALYZE`, `INSERT` de datos semilla y similares se clasifican en el troceado y no pasan por el parser.
6. **Perfilado y ajuste de la gramática.** `pprof` (CPU y memoria) sobre el corpus localiza las decisiones de predicción más caras (típicamente `expression`, `general_element` y `regular_id` con su lista enorme de palabras no reservadas); se refactorizan en el fork y se proponen upstream.
7. **Presupuesto por unidad.** Límite de tiempo (`context.WithTimeout` comprobado desde un listener de tokens) y de tokens; si se supera, la unidad se degrada a **indexado léxico** (declaraciones por cabecera y referencias por coincidencia de nombres en el catálogo), marcada con diagnóstico.

Alternativas descartadas para v1:

| Opción | Ventaja | Motivo para no elegirla ahora |
| --- | --- | --- |
| ANTLR en Java (JVM) | Runtime ANTLR más rápido y maduro | Requiere JVM o GraalVM; se mantiene solo como referencia de rendimiento en el PoC |
| ANTLR en Python o TypeScript | Encaja con stacks de scripting | Runtimes mucho más lentos con gramáticas de este tamaño |
| tree-sitter | Muy rápido, incremental | No hay gramática PL/SQL madura |
| Parser recursivo descendente propio en Go | Máximo rendimiento, sin dependencia de ANTLR | Coste de desarrollo alto; plan B de la fase 1 y objetivo de la fase 4 para el subconjunto caliente |

## Resolución semántica

La resolución se hace en dos pasadas separadas por una única barrera: primero se recogen todas las declaraciones del repositorio, y solo después se resuelven las referencias. Así una llamada a `HR.EMP_PKG.GET_SALARY` se resuelve aunque el paquete esté en un fichero que se procesa más tarde.

**Pasada 1 — declaraciones (paralela, por unidad).** Cada unidad produce un *resumen* pequeño y serializable: esquema y objeto; firmas de la spec (parámetros con nombre, tipo, modo y si tienen DEFAULT); tipos, variables, constantes, cursores y excepciones de paquete; columnas de tablas y vistas; sinónimos y secuencias. El resumen se cachea por hash de contenido.

**Catálogo global (barrera).** Los resúmenes se fusionan en un mapa inmutable `(ESQUEMA, NOMBRE) → objeto`, respetando que en Oracle tablas, vistas, secuencias, paquetes, subprogramas, tipos y sinónimos comparten un mismo espacio de nombres por esquema. Se completa con:

- Paquetes de sistema (`STANDARD`, `DBMS_OUTPUT`, `DBMS_SQL`, `UTL_FILE`…) como símbolos externos, generados a partir de un snapshot del diccionario.
- Columnas de tablas que no están en el repo, desde el snapshot del diccionario (ver sección de fuentes).

**Pasada 2 — referencias (paralela, por unidad).** Tabla de ámbitos anidados (bloques, subprogramas, bucles `FOR` con su índice implícito, registros de cursor `FOR`, etiquetas `<<lbl>>`). Orden de resolución de un nombre `a.b.c`, simplificado de las reglas de Oracle:

1. Ámbitos locales de dentro hacia fuera (variables, parámetros, etiquetas que cualifican nombres).
2. Miembros del paquete o tipo que contiene el código.
3. Objeto del esquema actual.
4. Sinónimo privado del esquema actual y después sinónimo `PUBLIC`.
5. Primer segmento como nombre de esquema (`HR.EMP_PKG…`).

Reglas que más afectan a la precisión:

- **Captura de columnas en SQL embebido.** Dentro de una sentencia SQL, un identificador se resuelve antes contra las columnas de las tablas y alias del `FROM` (incluidas CTE y subconsultas) que contra variables PL/SQL. Es la regla de Oracle y la fuente clásica de errores en herramientas.
- **Sobrecargas.** Se filtran candidatos por número de argumentos, notación nominal (`p_id => …`) y tipo de los literales. Si quedan varios, se emite una ocurrencia por candidato en el mismo rango y un diagnóstico de ambigüedad.
- **Sistema de tipos ligero.** Solo lo necesario para navegar: escalares, `record`, `%ROWTYPE` de tabla o cursor, colecciones, tipos objeto y `REF CURSOR`. `%TYPE` se resuelve siguiendo el ancla. Esto permite resolver `r.salary` o `l_tab(i).street`.
- **Triggers.** `:NEW` y `:OLD` se tipan como `%ROWTYPE` de la tabla del trigger, con `WriteAccess` en las asignaciones a `:NEW`.
- **Esquema actual.** Se toma del prefijo del `CREATE`, de una convención de rutas configurable (`db/<ESQUEMA>/…`) o de `ALTER SESSION SET CURRENT_SCHEMA` en el script.

Lo que no se resuelve se emite como referencia a un símbolo global en un paquete `unresolved`, de modo que otro índice que sí lo defina pueda enlazarlo, y cuenta para la métrica de tasa de resolución.

## Emisión SCIP

El índice nunca se construye entero en memoria: como `Index.documents` es un campo `repeated`, cada `Document` se serializa en la goroutine que lo resolvió y una única goroutine escritora concatena los registros en el fichero a medida que llegan.

Orden de escritura:

1. `Metadata`: `tool_info` (`scip-plsql`, versión, argumentos), `project_root`, codificación de texto.
2. Un `Document` por fichero fuente, en cuanto termina su pasada 2 (canal acotado entre trabajadores y escritor; serialización con google.golang.org/protobuf y los bindings Go de SCIP).
3. `external_symbols` al final: paquetes de sistema y objetos que solo vienen del diccionario.

Contenido de cada `Document`:

- `relative_path`, `language = PLSQL` (valor 70 del enum `Language`), `position_encoding = UTF8CodeUnitOffsetFromLineStart`.
- `occurrences` ordenadas por rango; rango de 3 elementos cuando cabe en una línea. En definiciones se rellena `enclosing_range` con el subprograma completo, útil para "código bajo el cursor" y para extraer contexto para agentes de IA.
- `symbols` con `display_name`, `kind`, `enclosing_symbol`, `signature_documentation` (la firma como bloque SQL) y `documentation` con el comentario inmediatamente anterior a la declaración (estilo PLDoc).
- `syntax_kind` opcional por flag: mejora el resaltado pero aumenta el tamaño del índice.
- `diagnostics` para unidades degradadas, código wrapped y sobrecargas ambiguas.

Determinismo: ocurrencias ordenadas por rango, símbolos por nombre y documentos por ruta (el escritor reordena con un buffer pequeño), sin marcas de tiempo. Así el índice es reproducible y los `Document` serializados se pueden cachear y reutilizar tal cual en ejecuciones incrementales.

Validación de salida con la CLI oficial: `scip lint`, `scip stats` y `scip snapshot` para tests de referencia.

## Fuentes: repositorio y diccionario de datos

**Recomendación:** modo híbrido. El repositorio es la fuente del texto (las posiciones deben coincidir con lo que ve el desarrollador) y un snapshot del diccionario completa lo que el repo no tiene. El indexador nunca se conecta a la base de datos durante el indexado: el snapshot se exporta aparte a JSON con un script y se versiona o se pasa como artefacto de CI.

| Fuente | Qué aporta | Limitación |
| --- | --- | --- |
| Repositorio (`.sql`, `.pks`, `.pkb`…) | Texto y posiciones reales; integración directa en CI | DDL de tablas incompleto o repartido en migraciones `ALTER TABLE` |
| Snapshot del diccionario (`ALL_TAB_COLUMNS`, `ALL_ARGUMENTS`, `ALL_SYNONYMS`, `ALL_OBJECTS`) | Estado compilado real: columnas, firmas de paquetes de sistema, sinónimos | No da posiciones en el repo; puede divergir de la rama indexada |
| PL/Scope (`ALL_IDENTIFIERS`, `ALL_STATEMENTS`) | La resolución de identificadores que hace el propio compilador, con línea y columna | Requiere recompilar con `PLSCOPE_SETTINGS='IDENTIFIERS:ALL'`; útil como referencia de validación, no en producción |

Migraciones: cuando el DDL viene de Flyway/Liquibase, la pasada 1 reproduce en orden `CREATE TABLE` y `ALTER TABLE ADD/DROP/RENAME COLUMN` para obtener el esquema final. Las definiciones de columna apuntan al `CREATE` o `ALTER` que las introdujo.

## Rendimiento

El parsing domina el coste, así que el diseño persigue tres cosas: parsear cada unidad una sola vez, hacerlo en todos los núcleos y no volver a parsear lo que no ha cambiado.

**Paralelismo.** Pool fijo de goroutines trabajadoras (`GOMAXPROCS`) alimentado por un canal de unidades, con `errgroup` para cancelación y propagación de errores; la tarea es la unidad de compilación, no el fichero. Las unidades se encolan de mayor a menor tamaño para que los paquetes enormes no queden al final. La IR de la pasada 1 se conserva para la pasada 2, evitando un segundo parseo; si se acerca al límite de memoria, se descarta y se re-parsea.

**Memoria.**

- Parse tree soltado tras construir la IR; la IR usa structs planos en slices con índices `int32` en lugar de punteros, lo que reduce trabajo del GC.
- Identificadores normalizados e internados con el paquete estándar `unique` (los mismos nombres se repiten millones de veces).
- `sync.Pool` para buffers de serialización y objetos de lexer/parser reutilizables.
- Documentos serializados y escritos en streaming; ningún índice completo en memoria.
- `GOMEMLIMIT` fijado según la máquina de CI y `GOGC` ajustado tras perfilar, para cambiar memoria por menos ciclos de GC.

**Incrementalidad.** Caché en disco indexada por hash de contenido de cada unidad, con tres niveles:

1. Resumen de declaraciones (pasada 1).
2. IR de la unidad.
3. `Document` SCIP serializado, junto con el conjunto de símbolos externos que referencia.

Un `Document` cacheado se reutiliza si su fichero no cambió y ninguno de los símbolos que referencia cambió de firma pública. Para eso se guarda una huella por símbolo exportado; un cambio solo en el body de un paquete no invalida a sus consumidores.

**Binario y runtime.** Binario estático (`CGO_ENABLED=0`) multiplataforma, sin arranque en frío ni calentamiento de JIT; solo el DFA de ANTLR necesita el corpus de calentamiento. Compilación con PGO (`default.pgo` generado desde el corpus de benchmarks), que optimiza las rutas calientes del parser.

**Paquetes gigantes.** Bodies de decenas de miles de líneas son habituales en código heredado y limitan el paralelismo. Mitigación posterior: trocear el body por subprogramas en el escáner, parseando cada uno con la regla de subprograma y el contexto de declaraciones del paquete.

Métricas a instrumentar desde el PoC: LOC/s total, ms por unidad (p50/p99), % de unidades resueltas en SLL, % de unidades degradadas, tasa de resolución de identificadores y pico de memoria.

## Casos difíciles y limitaciones

La mayoría de los casos difíciles se resuelven con degradación controlada: el índice siempre se genera, y lo que no se puede resolver con certeza queda marcado con un diagnóstico en lugar de producir una referencia falsa.

| Caso | Tratamiento |
| --- | --- |
| SQL dinámico (`EXECUTE IMMEDIATE`, `DBMS_SQL`, `OPEN … FOR`) | Si la cadena es literal o concatenación de constantes, se parsea como SQL con su offset; si no, opaca y diagnóstico |
| Código wrapped | Solo definición del objeto desde la cabecera |
| Compilación condicional | Flags configurables; ramas inactivas sin indexar |
| Variables de sustitución SQL\*Plus | Placeholders del mismo ancho |
| Sobrecargas ambiguas | Una ocurrencia por candidato y diagnóstico |
| Database links (`obj@dblink`) | Símbolo externo en un paquete SCIP por dblink |
| Identificadores entrecomillados y mayúsculas mixtas | Se conservan literalmente |
| Pseudocolumnas y funciones de `STANDARD` (`ROWNUM`, `LEVEL`, `SYSDATE`, `SQL%ROWCOUNT`) | Resueltas contra el paquete externo `SYS/STANDARD#` |
| Edition-Based Redefinition | Se ignora la edición; `EDITIONABLE` solo se acepta en la cabecera |
| Subprogramas externos (Java, C) | Definición con la firma PL/SQL; sin navegación al código externo |
| Oracle Forms, APEX | Fuera de alcance v1 (el PL/SQL va embebido en otros formatos) |

## Stack, estructura y validación

**Stack:** Go (última versión estable), runtime ANTLR `github.com/antlr4-go/antlr/v4` con el parser generado en un paquete propio, bindings oficiales `github.com/sourcegraph/scip/bindings/go/scip` sobre `google.golang.org/protobuf`, `cobra` para la CLI, `testing`/`go test -bench` y `pprof` para benchmarks y perfilado, y `goreleaser` para publicar binarios.

| Paquete Go | Responsabilidad |
| --- | --- |
| `internal/grammar` | Adaptador sobre el parser elegido en la fase 1 (omni/oracle o bytebase/parser/plsql), versión fijada por commit |
| `internal/splitter` | Escáner manual, clasificación de unidades, preprocesado con offsets |
| `internal/frontend` | Parsing SLL→LL, construcción de IR, degradación léxica |
| `internal/catalog` | Resúmenes, catálogo global, carga del snapshot del diccionario |
| `internal/resolver` | Ámbitos, reglas de nombres, tipos ligeros, SQL embebido |
| `internal/scipwriter` | Símbolos, ocurrencias, documentación, escritura en streaming |
| `internal/cache` | Caché por hash de contenido y huellas de firmas públicas |
| `cmd/scip-plsql` | CLI `scip-plsql index`, configuración YAML (globs, esquema por ruta, flags `$IF`) |

Estrategia de validación:

- **Snapshots de referencia** con `scip snapshot` sobre un corpus versionado: ejemplos de la propia gramática, los esquemas de ejemplo de Oracle (HR, OE, SH) y código real anonimizado de clientes.
- **Prueba diferencial contra PL/Scope.** En un contenedor Oracle Free se compila el corpus con PL/Scope activado y se comparan sus `ALL_IDENTIFIERS` con las ocurrencias del índice: precisión y cobertura de referencias por tipo de símbolo.
- **Benchmarks de regresión (go test -bench con benchstat)** en CI con las métricas de la sección de rendimiento, sobre un corpus grande fijo.
- **Robustez:** ficheros truncados, codificaciones mezcladas y sintaxis inválida no deben abortar el proceso; fuzzing nativo de Go (go test -fuzz) sobre el escáner de troceado y el preprocesado. Tests de concurrencia con el detector de carreras (-race).

## Librerías Go de soporte

La mayor reutilización está en el parser: Bytebase mantiene dos parsers Oracle en Go, uno recursivo descendente (`omni/oracle`) y otro ANTLR generado desde grammars-v4 (`parser/plsql`). Cualquiera de los dos evita generar y mantener la gramática nosotros. El resto se cubre con la librería estándar y una docena de dependencias consolidadas.

| Componente | Librería | Licencia | Uso en el indexador | Estado y riesgo |
| --- | --- | --- | --- | --- |
| Parser, opción A | [github.com/bytebase/omni/oracle](https://github.com/bytebase/omni) | MIT | Parser recursivo descendente en Go puro, sin cgo. Su AST cubre PL/SQL completo (`CreatePackageStmt`, `PLSQLBlock`, `PLSQLVarDecl`, `PLSQLCursorDecl`, `CreateTriggerStmt`, `CreateTypeStmt`…) y SQL embebido, con offsets de byte en cada nodo | Muy activo, pero sin versión etiquetada (pseudo-versiones). Para Oracle solo publica `ast` y `parser`, sin catálogo ni resolución de nombres. Hay que medir su cobertura sobre el corpus |
| Parser, opción B | [github.com/bytebase/parser/plsql](https://pkg.go.dev/github.com/bytebase/parser) | BSD-3-Clause | Parser ANTLR generado desde grammars-v4 y mantenido por Bytebase; sustituye a nuestro fork de la gramática | Activo, sin versión etiquetada. Sucesor del antiguo `bytebase/plsql-parser` |
| Runtime ANTLR | [github.com/antlr4-go/antlr/v4](https://github.com/antlr/antlr4/blob/master/doc/go-target.md) | BSD-3-Clause | Solo si se usa la opción B | Repo oficial del runtime Go desde la 4.13 |
| SCIP | [github.com/sourcegraph/scip/bindings/go/scip](https://pkg.go.dev/github.com/sourcegraph/scip/bindings/go/scip@v0.5.2) | Apache-2.0 | Tipos protobuf; formateo y parseo de símbolos (`SymbolFormatter`, `ParseSymbol`); `CanonicalizeSymbol`, `FlattenSymbols`; `IndexVisitor` para leer índices en streaming en los tests | Oficial |
| CLI SCIP | `scip` (mismo repo) | Apache-2.0 | `lint`, `snapshot` y `test` con ficheros anotados, en CI | Oficial; incluye `scip test --check-documents` |
| Protobuf | google.golang.org/protobuf | BSD-3-Clause | Serialización de cada `Document` | Estándar de facto |
| Driver Oracle | [github.com/sijms/go-ora](https://github.com/sijms/go-ora) | MIT | `export-catalog` y `extract` sin Instant Client ni cgo | Go puro; la rama principal ya es v3 (cambio de import path). Alternativa con cgo: `godror` |
| Codificación | golang.org/x/text/encoding/charmap + `unicode/utf8` | BSD-3-Clause | Validar UTF-8 y, si falla, transcodificar desde Windows-1252 o ISO-8859-15 | Oficial (x/) |
| Recorrido de ficheros | [github.com/bmatcuk/doublestar/v4](https://github.com/bmatcuk/doublestar) | MIT | Globs con `**` sobre `io/fs` | Muy extendido |
| Exclusiones | github.com/go-git/go-git/v5 (paquete `plumbing/format/gitignore`) | Apache-2.0 | Respetar `.gitignore` sin reimplementarlo | Maduro |
| Concurrencia | golang.org/x/sync/errgroup | BSD-3-Clause | Pool de trabajadores con cancelación y límite (`SetLimit`) | Oficial (x/) |
| Caché | github.com/cespare/xxhash/v2, github.com/klauspost/compress/zstd, go.etcd.io/bbolt | MIT, BSD-3-Clause, MIT | Hash de contenido, compresión de IR y `Document` cacheados, índice de la caché en un fichero | Muy extendidos |
| CLI y configuración | github.com/spf13/cobra, go.yaml.in/yaml/v3 | Apache-2.0, MIT/Apache-2.0 | Subcomandos y configuración YAML | `go.yaml.in/yaml/v3` es la continuación mantenida de `gopkg.in/yaml.v3`; confirmar al fijar dependencias |
| Tests | github.com/google/go-cmp, github.com/testcontainers/testcontainers-go, golang.org/x/perf (`benchstat`) | BSD-3-Clause, MIT, BSD-3-Clause | Comparación de estructuras; Oracle Free en contenedor (imagen `gvenzl/oracle-free`) para PL/Scope y `extract`; comparación estadística de benchmarks | Maduros; testcontainers-go con contenedor genérico para Oracle |
| Publicación | goreleaser | MIT | Binarios multiplataforma y checksums | Estándar de facto |

De la librería estándar se usan, sin dependencias extra: `unique` (internado), `sync.Pool`, `slices` (ordenación determinista), `log/slog`, fuzzing de `testing`, `runtime/pprof` y PGO.

**Impacto en el diseño:** la fase 1 compara las opciones A y B sobre el mismo corpus (cobertura, LOC/s, memoria y calidad de offsets). Si `omni/oracle` cubre el corpus, pasa a ser el parser principal: desaparecen el fork de la gramática, la estrategia SLL→LL y el riesgo del runtime ANTLR, y el plan B de parser propio queda prácticamente resuelto. Antes de escribir el escáner de troceado propio, conviene revisar también si la función `Split` del paquete raíz de `omni/oracle` cubre los casos SQL\*Plus.

## Escenario de pruebas y corpus PL/SQL

**Propuesta:** un corpus en cinco niveles. Su base son 11 repositorios open source ya medidos (unas 594.000 líneas en 2.468 ficheros), con Oracle Free y PL/Scope como verdad de referencia. Se completa con un generador sintético para volumen y con código real anonimizado de un cliente. Cada nivel mide una cosa distinta y tiene su propio criterio de aceptación.

| Nivel | Contenido | Tamaño | Qué mide | Criterio de aceptación |
| --- | --- | --- | --- | --- |
| N0 · Casos unitarios | Ficheros pequeños escritos a mano, uno por construcción: sobrecargas, captura de columnas, sinónimos, `%ROWTYPE`, `$IF`, q-quotes, `&var`, wrapped, triggers con `:NEW` | \~200 ficheros | Corrección exacta de símbolos, roles y rangos | 100 % de las expectativas de `scip test` con ficheros anotados |
| N1 · Open source | Los 11 repositorios de la tabla siguiente, fijados por commit | \~594.000 líneas | Robustez y cobertura sintáctica en código variado | Ningún fallo del proceso; % de unidades sin degradar medido en la fase 1 como línea base, sin retrocesos entre versiones |
| N2 · Referencia Oracle | Subconjunto de N1 instalable (esquemas de ejemplo, utPLSQL, PL/JSON, Logger) compilado en Oracle Free con PL/Scope | A medir tras instalar | Precisión y cobertura de referencias frente al compilador | ≥ 95 % de identificadores resueltos igual que PL/Scope |
| N3 · Volumen sintético | Generador determinista con semilla: replica N1 en varios esquemas renombrados, crea paquetes de 20.000–50.000 líneas, sinónimos públicos, ficheros Windows-1252 y código wrapped | Escalable de 1 M a 10 M de líneas | Rendimiento, memoria e incrementalidad | 1 M LOC en menos de 2 min con 16 núcleos; PR típico en menos de 30 s |
| N4 · Código real | Uno o dos sistemas de cliente anonimizados, bajo acuerdo de confidencialidad, fuera del repo público | El del cliente | Validez en el caso de uso real | Mismas métricas que N1 y N2 |

**Corpus open source medido** (clonado y analizado el 5 de noviembre de 2026; conteos por expresiones regulares, que incluyen duplicados de los scripts de instalación):

| Repositorio | Licencia | Ficheros | Líneas | Paquetes spec/body | Tipos | Triggers | `$IF` | `EXECUTE IMMEDIATE` | Qué aporta |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| [oracle-samples/oracle-db-examples](https://github.com/oracle-samples/oracle-db-examples) | Apache-2.0 | 1.513 | 299.426 | 101/90 | 96 | 69 | 0 | 1.044 | Máxima variedad de SQL moderno; scripts de carga masiva que el troceado debe saltar |
| [oracle-samples/db-sample-schemas](https://github.com/oracle-samples/db-sample-schemas) | MIT | 89 | 119.327 | 5/4 | 29 | 7 | 0 | 12 | Esquemas HR, OE, SH y CO: DDL de tablas y vistas; base de N2 |
| [utPLSQL/utPLSQL](https://github.com/utPLSQL/utPLSQL) | Apache-2.0 | 487 | 64.412 | 234/203 | 240 | 3 | 7 | 748 | Muchos tipos objeto con herencia; SQL dinámico intensivo |
| [mortenbra/alexandria-plsql-utils](https://github.com/mortenbra/alexandria-plsql-utils) | Sin LICENSE en la raíz; revisar | 160 | 40.529 | 56/56 | 9 | 0 | 4 | 7 | Paquetes utilitarios clásicos; 3 ficheros no UTF-8 |
| [ogobrecht/console](https://github.com/ogobrecht/console) | MIT | 43 | 20.374 | 15/14 | 4 | 0 | 10 | 52 | Uso intensivo de `%TYPE` y `%ROWTYPE` |
| [pljson/pljson](https://github.com/pljson/pljson) | MIT | 70 | 16.003 | 20/20 | 28 | 0 | 0 | 48 | Tipos objeto con métodos sobrecargados |
| [OraOpenSource/OOS-Utils](https://github.com/OraOpenSource/OOS-Utils) | MIT | 32 | 14.273 | 21/21 | 0 | 1 | 16 | 15 | Script de instalación que repite objetos: prueba de definiciones duplicadas |
| [ogobrecht/plex](https://github.com/ogobrecht/plex) | MIT | 14 | 8.575 | 3/3 | 0 | 0 | 44 | 18 | Compilación condicional densa |
| [OraOpenSource/Logger](https://github.com/OraOpenSource/Logger) | MIT | 29 | 5.961 | 2/2 | 0 | 2 | 95 | 40 | La mayor densidad de `$IF` del corpus |
| [osalvador/tePLSQL](https://github.com/osalvador/tePLSQL) | MIT | 17 | 4.627 | 6/6 | 0 | 0 | 28 | 1 | Plantillas con muchos `&`: prueba de variables de sustitución frente a texto literal |
| [utPLSQL/utPLSQL-demo-project](https://github.com/utPLSQL/utPLSQL-demo-project) | Apache-2.0 | 14 | 337 | 3/3 | 0 | 0 | 0 | 0 | Proyecto mínimo para el smoke test de CI |
| **Total** |  | **2.468** | **593.844** | **466/422** | **406** | **82** | **204** | **1.985** |  |

**Huecos que la medición deja al descubierto y cómo se cubren:**

- **Código wrapped:** solo aparece 1 caso. N3 genera wrapped compilando en Oracle Free y extrayendo con `DBMS_DDL.WRAP`.
- **Paquetes monolíticos:** los ficheros más largos del corpus son cargas de datos (93.476 líneas de INSERT en un único script), no código. El paquete escrito a mano más grande ronda las 7.000 líneas. N3 genera bodies de 20.000–50.000 líneas, y N4 aporta los reales.
- **Codificaciones heredadas:** solo 29 ficheros no son UTF-8. N3 transcodifica una parte del corpus a Windows-1252 e ISO-8859-15.
- **Grafo entre esquemas y sinónimos públicos:** el código open source vive casi siempre en un único esquema. N3 replica N1 en varios esquemas con sinónimos cruzados.

**Montaje del entorno:**

1. Repositorio `plsql-corpus` con un `manifest.yaml` por fuente: URL, commit fijado, licencia, globs de inclusión y nivel. Un script lo descarga de forma reproducible; el código de terceros no se versiona dentro.
2. Job de CI nocturno que indexa N1 y N3, publica las métricas con `benchstat` y falla si la tasa de unidades sin degradar o el rendimiento empeoran respecto a la línea base.
3. Job semanal de N2: levanta Oracle Free con testcontainers-go (imagen `gvenzl/oracle-free`) e instala el subconjunto con `PLSCOPE_SETTINGS='IDENTIFIERS:ALL, STATEMENTS:ALL'`. Después exporta `ALL_IDENTIFIERS` a JSON y lo compara con el índice por tipo de símbolo y uso.
4. N4 se ejecuta fuera del CI público. La anonimización usa el lexer del propio indexador: renombra identificadores de forma consistente con un hash con sal, vacía literales y comentarios, y conserva estructura, longitudes de línea y dependencias.

```yaml
# plsql-corpus/manifest.yaml (extracto)
- name: utplsql
  url: https://github.com/utPLSQL/utPLSQL
  commit: <sha fijado>
  license: Apache-2.0
  include: ["source/**/*.pk?", "source/**/*.tp?", "source/**/*.sql"]
  levels: [N1, N2]
```

**Pregunta abierta:** qué cliente o sistema interno puede aportar el corpus N4 y bajo qué acuerdo.

## Plan de construcción con TDD

**Propuesta:** TDD *outside-in* con dos bucles.

- **Bucle externo, de aceptación.** Cada incremento empieza con un fichero PL/SQL anotado del nivel N0 que falla en `scip test`: el ciclo rojo-verde se hace sobre el índice que verá el usuario.
- **Bucle interno, unitario.** Tests table-driven de Go guían cada paquete hasta que la aceptación pasa.

Los corpus N1–N4 no guían el desarrollo. Actúan como red de regresión y como fuente de nuevos casos N0.

### Tipos de test

| Tipo | Herramienta | Qué fija | Cuándo se escribe | Dónde corre |
| --- | --- | --- | --- | --- |
| Aceptación | Ficheros `.sql` anotados + `scip test` | Símbolo, rol y rango exactos de cada ocurrencia | Antes de cada incremento | Cada PR |
| Unitario | `go test`, table-driven, `go-cmp` | Comportamiento de splitter, resolver, generación de símbolos y caché | Durante el incremento, antes del código | Cada PR |
| Contrato del parser | La misma suite contra los adaptadores `omni/oracle` y `bytebase/parser/plsql` | Que ambos producen la misma IR | Fase 1 | Cada PR |
| Propiedades y fuzz | `go test -fuzz` | Invariantes: unidades concatenadas = fichero, offsets en rango, ningún panic | Con cada componente que procesa texto | 30 s por objetivo en PR; largo cada noche |
| Snapshot | `scip snapshot` + golden con flag `-update` | Salida completa de un subconjunto de N1 | Al estabilizar cada fase | Cada PR |
| Determinismo | Indexar dos veces y comparar bytes | Reproducibilidad del `index.scip` | Incremento 1 | Cada PR |
| Diferencial | PL/Scope (N2) | Precisión frente al compilador | No guía el diseño; valida | Semanal |
| Rendimiento | `go test -bench` + `benchstat` sobre N1/N3 | Que LOC/s y memoria no retroceden | Desde la fase 1 | Cada noche |

### Forma de un test de aceptación

El caso fija la regla de captura de columnas: dentro del `UPDATE`, `salary` es la columna de `EMPLOYEES`, no el parámetro homónimo. Los comentarios `--` con `^` marcan el rango y el símbolo esperados, en el formato de ficheros de test de SCIP con la sintaxis de comentario configurada para SQL. La tabla se define en otro fichero del mismo directorio de fixtures.

```sql
CREATE OR REPLACE PROCEDURE hr.raise_all(salary NUMBER) IS
--                             ^^^^^^^^^ definition scip-plsql oracle demo 1.0 HR/RAISE_ALL().
--                                       ^^^^^^ definition scip-plsql oracle demo 1.0 HR/RAISE_ALL().(SALARY)
BEGIN
  UPDATE employees SET salary = salary * 1.1;
--       ^^^^^^^^^ reference scip-plsql oracle demo 1.0 HR/EMPLOYEES#
--                     ^^^^^^ reference scip-plsql oracle demo 1.0 HR/EMPLOYEES#SALARY.
--                              ^^^^^^ reference scip-plsql oracle demo 1.0 HR/EMPLOYEES#SALARY.
END;
/
```

Estructura en el repo: `testdata/accept/<construcción>/*.sql`, un directorio por regla, más un `catalog.json` cuando el caso necesita objetos externos.

### Incrementos verticales

Cada incremento atraviesa el pipeline completo, desde el fichero hasta el `index.scip`, y termina con su suite N0 en verde, sin retrocesos en N1.

| # | Incremento | Primer test en rojo | Unitarios que lo guían | Hecho cuando |
| --- | --- | --- | --- | --- |
| 1 | Esqueleto de extremo a extremo | Un `CREATE PROCEDURE` vacío produce un `Document` con su definición | CLI, escritor SCIP, símbolos | `scip lint` limpio; test de determinismo en verde |
| 2 | Troceado de scripts | Script con 3 unidades, `/`, comentarios y q-quotes; cada definición en su offset | Splitter table-driven y fuzz de invariantes | Unidades de N1 detectadas sin pérdidas |
| 3 | Paquetes, spec y body | `ForwardDefinition` en la spec, `Definition` en el body, sobrecargas `(+n)` | Adaptador de parser, generación de descriptores | Suite de contrato en verde con ambos parsers |
| 4 | Ámbitos locales | Variables, parámetros, índice de `FOR` y etiquetas como `local N` | Tabla de ámbitos | Resolución local completa en N0 |
| 5 | Catálogo y referencias globales | Llamada entre paquetes y esquemas; sinónimo privado frente a `PUBLIC` | Catálogo, orden de resolución | Primera tasa de resolución medida en N1 |
| 6 | SQL embebido | Tablas, alias, CTE, captura de columnas, `WriteAccess` en `INSERT`/`UPDATE`/`MERGE` | Resolvedor de ámbitos SQL | Caso de captura de columnas en verde |
| 7 | Tipos ligeros | `%TYPE`, `%ROWTYPE`, records, colecciones, tipos objeto, `:NEW`/`:OLD` | Sistema de tipos | `r.col` y `t(i).attr` resueltos |
| 8 | Preprocesado | `$IF` con flags, `&var`, wrapped, Windows-1252 | Preprocesador con offsets preservados | Fixtures N3 de codificación y wrapped en verde |
| 9 | Degradación y robustez | Unidad que agota el presupuesto pasa a indexado léxico con diagnóstico | Presupuesto, indexado léxico | Ningún fallo en N1 ni en el fuzz nocturno |
| 10 | Incremental y caché | Segunda ejecución sin cambios reutiliza el 100 % de los `Document` | Caché, huellas de firmas | Un PR típico de N3 en menos de 30 s |
| 11 | `export-catalog` y `extract` | Volcado de Oracle Free igual al fixture esperado | Driver go-ora (build tag `integration`) | N2 en marcha |

Los incrementos 1–3 forman la fase 1 del roadmap. Como la suite de aceptación es independiente del parser, la misma batería decide la comparación entre `omni/oracle` y `bytebase/parser/plsql`: gana el que pasa más casos N0 con mejor rendimiento en N1. Los incrementos 4–8 forman la fase 2; el 9 y el 10, la fase 3.

### Reglas de trabajo

- **Todo fallo encontrado en N1–N4 se convierte primero en un caso N0** mínimo (reducido línea a línea hasta el mínimo que reproduce) y después se corrige. Así el corpus grande alimenta la suite pequeña y rápida.
- **Los golden solo se regeneran con `-update`**, y el diff forma parte de la revisión del PR.
- **El parser queda detrás de una interfaz `frontend.Parser`.** Cambiar de implementación (opción A, B o parser propio) no toca los tests de aceptación.
- **La suite de cada PR debe tardar menos de dos minutos.** N1 completo, fuzz largo y N3 van al job nocturno; N2, al semanal.
- **La cobertura de líneas no es el objetivo.** Lo es el catálogo de construcciones N0: cada regla del diseño (secciones de modelo SCIP y resolución) tiene al menos un fichero de aceptación que la nombra.

## Roadmap

Cuatro fases; no se pasa a la siguiente sin medir la métrica de su puerta. El PoC de la fase 1 es el que confirma o corrige la apuesta por ANTLR en Go antes de invertir en la resolución; si no alcanza los objetivos, el parser propio tolerante se adelanta desde la fase 4.

&#91;embedded content: roadmap · 4 fases, 3 puertas\]

Preguntas abiertas: tamaño y forma del primer corpus real (¿qué cliente o repo interno?), consumidor principal del índice (Sourcegraph, agentes de IA o ambos) y versión mínima de Oracle a soportar.

## Fuentes

- [Gramática PL/SQL de grammars-v4](https://github.com/antlr/grammars-v4/tree/master/sql/plsql) (referencia indicada)
- [bytebase/omni](https://github.com/bytebase/omni) y [su AST Oracle PL/SQL](https://pkg.go.dev/github.com/bytebase/omni/oracle/ast)
- [bytebase/parser](https://pkg.go.dev/github.com/bytebase/parser): monorepo de parsers ANTLR en Go, incluido `plsql`
- [bytebase/plsql-parser](https://github.com/bytebase/plsql-parser): parser PL/SQL ANTLR anterior
- [Runtime Go de ANTLR](https://github.com/antlr/antlr4/blob/master/doc/go-target.md)
- [Bindings Go de SCIP v0.5.2](https://pkg.go.dev/github.com/sourcegraph/scip/bindings/go/scip@v0.5.2) y [enums `Language` (PLSQL = 70)](https://pkg.go.dev/github.com/sourcegraph/scip@v0.2.1/bindings/go/scip)
- [Changelog de SCIP v0.6.0](https://github.com/sourcegraph/scip/pull/340/files)
- [Copia de `scip.proto` con `ForwardDefinition = 64`](https://docs.rs/crate/lip-cli/latest/source/src/proto/scip.proto)
- [go-ora](https://github.com/sijms/go-ora)
- [doublestar](https://github.com/bmatcuk/doublestar)
- [Guía de uso de la gramática con `PlSqlLexerBase` / `PlSqlParserBase`](https://gist.github.com/eeskildsen/6ce49c5496f10c3e06ff6bde765f1424)
