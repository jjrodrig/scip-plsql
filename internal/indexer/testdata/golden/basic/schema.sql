  CREATE TABLE hr.employees (id NUMBER, salary NUMBER);
--                ^^^^^^^^^ definition scip-plsql oracle demo 1 HR/EMPLOYEES#
--                           ^^ definition scip-plsql oracle demo 1 HR/EMPLOYEES#ID.
--                                      ^^^^^^ definition scip-plsql oracle demo 1 HR/EMPLOYEES#SALARY.
  --              ^^^^^^^^^ definition scip-plsql oracle demo 1 HR/EMPLOYEES#
  --                         ^^ definition scip-plsql oracle demo 1 HR/EMPLOYEES#ID.
  --                                    ^^^^^^ definition scip-plsql oracle demo 1 HR/EMPLOYEES#SALARY.
  CREATE PACKAGE hr.payroll AS
--                  ^^^^^^^ forward_definition scip-plsql oracle demo 1 HR/PAYROLL#
  --                ^^^^^^^ forward_definition scip-plsql oracle demo 1 HR/PAYROLL#
    PROCEDURE raise_all(p_value NUMBER);
--            ^^^^^^^^^ forward_definition scip-plsql oracle demo 1 HR/PAYROLL#RAISE_ALL().
--                      ^^^^^^^ forward_definition scip-plsql oracle demo 1 HR/PAYROLL#RAISE_ALL().(P_VALUE)
  --          ^^^^^^^^^ forward_definition scip-plsql oracle demo 1 HR/PAYROLL#RAISE_ALL().
  --                    ^^^^^^^ forward_definition scip-plsql oracle demo 1 HR/PAYROLL#RAISE_ALL().(P_VALUE)
  END;
  /
  
