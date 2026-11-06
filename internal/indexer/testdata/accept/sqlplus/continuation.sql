CREATE TABLE hr.products (id NUMBER, name VARCHAR2(50));
--              ^^^^^^^^ definition scip-plsql oracle demo 1 HR/PRODUCTS#
--                        ^^ definition scip-plsql oracle demo 1 HR/PRODUCTS#ID.
--                                   ^^^^ definition scip-plsql oracle demo 1 HR/PRODUCTS#NAME.
INSERT INTO hr.products (id, name) VALUES (1-
--             ^^^^^^^^ reference scip-plsql oracle demo 1 HR/PRODUCTS#
--                       ^^ reference scip-plsql oracle demo 1 HR/PRODUCTS#ID.
--                           ^^^^ reference scip-plsql oracle demo 1 HR/PRODUCTS#NAME.
, 'a'||-
'b');
