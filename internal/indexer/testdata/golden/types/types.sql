  CREATE TABLE hr.emp (id NUMBER, salary NUMBER);
--                ^^^ definition scip-plsql oracle demo 1 HR/EMP#
--                     ^^ definition scip-plsql oracle demo 1 HR/EMP#ID.
--                                ^^^^^^ definition scip-plsql oracle demo 1 HR/EMP#SALARY.
  --              ^^^ definition scip-plsql oracle demo 1 HR/EMP#
  --                              ^^^^^^ definition scip-plsql oracle demo 1 HR/EMP#SALARY.
  CREATE PROCEDURE hr.p IS
--                    ^ definition scip-plsql oracle demo 1 HR/P().
--                    documentation
--                    > ^^^ definition scip-plsql oracle demo 1 HR/EMP#
--                    > ^^^^^^ definition scip-plsql oracle demo 1 HR/EMP#SALARY.
  --                  ^ definition scip-plsql oracle demo 1 HR/P().
    r hr.emp%ROWTYPE;
--  ^ definition local 0
--  documentation
--  > ^ definition scip-plsql oracle demo 1 HR/P().
--  relationship scip-plsql oracle demo 1 HR/EMP# type_definition
--       ^^^ reference scip-plsql oracle demo 1 HR/EMP#
  --^ definition local 0
  --     ^^^ reference scip-plsql oracle demo 1 HR/EMP#
  BEGIN
    r.salary := 1;
--  ^ reference local 0
--    ^^^^^^ reference scip-plsql oracle demo 1 HR/EMP#SALARY.
  --^ reference local 0
  --  ^^^^^^ reference scip-plsql oracle demo 1 HR/EMP#SALARY.
  END;
  /
  CREATE TRIGGER hr.trg BEFORE UPDATE ON hr.emp FOR EACH ROW
--                  ^^^ definition scip-plsql oracle demo 1 HR/TRG.
--                                          ^^^ reference scip-plsql oracle demo 1 HR/EMP#
  --                ^^^ definition scip-plsql oracle demo 1 HR/TRG.
  --                                        ^^^ reference scip-plsql oracle demo 1 HR/EMP#
  BEGIN
    :new.salary := :old.salary;
--       ^^^^^^ reference scip-plsql oracle demo 1 HR/EMP#SALARY.
--                      ^^^^^^ reference scip-plsql oracle demo 1 HR/EMP#SALARY.
  --     ^^^^^^ reference scip-plsql oracle demo 1 HR/EMP#SALARY.
  --                    ^^^^^^ reference scip-plsql oracle demo 1 HR/EMP#SALARY.
  END;
  /
  
