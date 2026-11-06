  CREATE TYPE hr.base_t AS OBJECT (amount NUMBER) NOT FINAL;
--               ^^^^^^ forward_definition scip-plsql oracle demo 1 HR/BASE_T#
--                                 ^^^^^^ forward_definition scip-plsql oracle demo 1 HR/BASE_T#AMOUNT.
  --             ^^^^^^ forward_definition scip-plsql oracle demo 1 HR/BASE_T#
  --                               ^^^^^^ forward_definition scip-plsql oracle demo 1 HR/BASE_T#AMOUNT.
  /
  CREATE TYPE hr.child_t UNDER hr.base_t (extra NUMBER, MEMBER PROCEDURE run);
--               ^^^^^^^ forward_definition scip-plsql oracle demo 1 HR/CHILD_T#
--                                ^^^^^^ reference scip-plsql oracle demo 1 HR/BASE_T#
--                                        ^^^^^ forward_definition scip-plsql oracle demo 1 HR/CHILD_T#EXTRA.
--                                                                       ^^^ forward_definition scip-plsql oracle demo 1 HR/CHILD_T#RUN().
  --             ^^^^^^^ forward_definition scip-plsql oracle demo 1 HR/CHILD_T#
  --                              ^^^^^^ reference scip-plsql oracle demo 1 HR/BASE_T#
  --                                      ^^^^^ forward_definition scip-plsql oracle demo 1 HR/CHILD_T#EXTRA.
  --                                                                     ^^^ forward_definition scip-plsql oracle demo 1 HR/CHILD_T#RUN().
  /
  CREATE TYPE BODY hr.child_t AS
--                    ^^^^^^^ definition scip-plsql oracle demo 1 HR/CHILD_T#
--                    relationship scip-plsql oracle demo 1 HR/BASE_T# implementation
  --                  ^^^^^^^ definition scip-plsql oracle demo 1 HR/CHILD_T#
   MEMBER PROCEDURE run IS BEGIN SELF.amount := extra; END;
--                  ^^^ definition scip-plsql oracle demo 1 HR/CHILD_T#RUN().
--                               ^^^^ reference local 0
--                                    ^^^^^^ reference scip-plsql oracle demo 1 HR/BASE_T#AMOUNT.
--                                              ^^^^^ reference scip-plsql oracle demo 1 HR/CHILD_T#EXTRA.
  --                ^^^ definition scip-plsql oracle demo 1 HR/CHILD_T#RUN().
  --                                  ^^^^^^ reference scip-plsql oracle demo 1 HR/BASE_T#AMOUNT.
  --                                            ^^^^^ reference scip-plsql oracle demo 1 HR/CHILD_T#EXTRA.
  END;
  /
  CREATE PROCEDURE hr.client IS r hr.child_t; BEGIN r.run(); END;
--                    ^^^^^^ definition scip-plsql oracle demo 1 HR/CLIENT().
--                              ^ definition local 1
--                              relationship scip-plsql oracle demo 1 HR/CHILD_T# type_definition
--                                   ^^^^^^^ reference scip-plsql oracle demo 1 HR/CHILD_T#
--                                                  ^ reference local 1
--                                                    ^^^ reference scip-plsql oracle demo 1 HR/CHILD_T#RUN().
  --                  ^^^^^^ definition scip-plsql oracle demo 1 HR/CLIENT().
  --                                 ^^^^^^^ reference scip-plsql oracle demo 1 HR/CHILD_T#
  --                                                  ^^^ reference scip-plsql oracle demo 1 HR/CHILD_T#RUN().
  /
  
